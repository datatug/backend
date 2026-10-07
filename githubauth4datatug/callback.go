// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"errors"
	"net/http"
	"net/url"
)

// ScrubOAuthCallbackURL must be the first callback-handler operation. It
// extracts the one-time code/state in memory, then removes every query value
// from both URL representations before application logging or routing.
func ScrubOAuthCallbackURL(request *http.Request) (code, state string, err error) {
	if request == nil || request.URL == nil {
		return "", "", ErrOAuthStateInvalid
	}
	query, parseErr := url.ParseQuery(request.URL.RawQuery)
	request.URL.RawQuery = ""
	request.URL.ForceQuery = false
	request.RequestURI = request.URL.RequestURI()
	if parseErr != nil {
		return "", "", ErrOAuthStateInvalid
	}
	codeValues, stateValues := query["code"], query["state"]
	if len(codeValues) != 1 || len(stateValues) != 1 || codeValues[0] == "" || stateValues[0] == "" {
		return "", "", ErrOAuthStateInvalid
	}
	if len(query["error"]) > 0 {
		return "", "", errors.New("GitHub authorization was not completed")
	}
	return codeValues[0], stateValues[0], nil
}
