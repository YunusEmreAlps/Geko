package investigation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

func EncodeGroupKey(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) }
func DecodeGroupKey(token string) (string, error) {
	if len(token) > 8192 {
		return "", errors.New("group key is too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	var fields []json.RawMessage
	if err != nil || json.Unmarshal(decoded, &fields) != nil || len(fields) != 8 {
		return "", errors.New("invalid group key")
	}
	return string(decoded), nil
}

func EncodeGroupCursor(cursor *GroupCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}
func DecodeGroupCursor(token string) (*GroupCursor, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) > 16384 {
		return nil, errors.New("group cursor is too long")
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var cursor GroupCursor
	if err != nil || json.Unmarshal(data, &cursor) != nil {
		return nil, errors.New("invalid group cursor")
	}
	if _, err := DecodeGroupKey(EncodeGroupKey(cursor.Key)); err != nil {
		return nil, err
	}
	return &cursor, nil
}
