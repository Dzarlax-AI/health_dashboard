package accessauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// ParseUserMap parses an exact JSON object and rejects duplicate email keys.
func ParseUserMap(raw string) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, errors.New("expected a JSON object")
	}
	users := map[string]string{}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		email, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("email keys must be strings")
		}
		if _, exists := users[email]; exists {
			return nil, errors.New("duplicate email key")
		}
		var username string
		if err := dec.Decode(&username); err != nil {
			return nil, errors.New("usernames must be strings")
		}
		users[email] = username
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("unexpected data after JSON object")
	}
	if len(users) == 0 {
		return nil, errors.New("object must contain at least one user")
	}
	return users, nil
}
