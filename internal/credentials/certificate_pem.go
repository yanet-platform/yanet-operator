package credentials

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// scanCertificatePEM checks each certificate block independently: pem.Decode
// can skip a malformed block if it finds a later well-formed one. Other PEM
// types and surrounding comments are allowed, as in the native loaders. The
// native key-pair loader additionally rejects an unmatched BEGIN in the tail.
func scanCertificatePEM(data []byte, chain bool, visit func(*pem.Block) error) error {
	begin := []byte("-----BEGIN")
	lineBegin := []byte("\n-----BEGIN")
	if !bytes.HasPrefix(data, begin) {
		idx := bytes.Index(data, lineBegin)
		if idx < 0 {
			if chain && bytes.Contains(data, begin) {
				return fmt.Errorf("malformed PEM tail")
			}
			return nil
		}
		data = data[idx+1:]
	}
	tailHasBegin := false
	for len(data) > 0 {
		end := len(data)
		if idx := bytes.Index(data, lineBegin); idx >= 0 {
			end = idx + 1
		}
		section := data[:end]
		block, tail := pem.Decode(section)
		line, _, _ := bytes.Cut(section, []byte{'\n'})
		label := bytes.TrimRight(bytes.TrimSpace(line), "-")
		if block == nil && (bytes.Equal(label, []byte("-----BEGIN CERTIFICATE")) || bytes.HasPrefix(line, []byte("-----BEGIN CERTIFICATE-----"))) {
			return fmt.Errorf("malformed PEM certificate block")
		}
		if block != nil && block.Type == "CERTIFICATE" && visit != nil {
			if err := visit(block); err != nil {
				return err
			}
		}
		tailHasBegin = bytes.Contains(tail, begin)
		data = data[end:]
	}
	if chain && tailHasBegin {
		return fmt.Errorf("malformed PEM tail")
	}
	return nil
}

func trustRoots(data []byte) (*x509.CertPool, error) {
	roots := x509.NewCertPool()
	usable := false
	err := scanCertificatePEM(data, false, func(block *pem.Block) error {
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return fmt.Errorf("parse trust certificate: %w", parseErr)
		}
		if !certificate.BasicConstraintsValid || !certificate.IsCA {
			return fmt.Errorf("trust certificate is not a CA: %s", certificate.Subject)
		}
		// The native auth store parses all certificate blocks. The native
		// transport's AppendCertsFromPEM only trusts blocks without headers.
		if len(block.Headers) == 0 {
			roots.AddCert(certificate)
			usable = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("trust bundle: %w", err)
	}
	if !usable {
		return nil, fmt.Errorf("trust bundle contains no usable PEM certificates")
	}
	return roots, nil
}
