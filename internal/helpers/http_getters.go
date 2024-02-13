package helpers

import (
	"io"
	"net/http"
)

// HttpGet make simple get request and return string result.
func HttpGet(uri string) (error, string) {
	resp, err := http.Get(uri)
	if err != nil {
		return err, ""
	} else {
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err, ""
		} else {
			res := string(body)
			return nil, res
		}
	}
}
