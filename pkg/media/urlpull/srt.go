// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package urlpull

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/livekit/protocol/utils/netguard"

	"github.com/livekit/ingress/pkg/errors"
)

const srtLookupTimeout = 5 * time.Second

var lookupNetIP = net.DefaultResolver.LookupNetIP

var allowedSRTOptions = map[string]bool{
	"mode":               true,
	"streamid":           true,
	"passphrase":         true,
	"pbkeylen":           true,
	"latency":            true,
	"rcvlatency":         true,
	"peerlatency":        true,
	"enforcedencryption": true,
	"conntimeo":          true,
}

// srtclientsrc splits the query on '&' only, keeps the last duplicate key and leaves '+' undecoded,
// so the URL is rebuilt from the checked parts rather than passed through.
func pinSRTURL(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "srt" {
		return "", errors.ErrInvalidSRTURL
	}

	query, err := parseSRTQuery(u.RawQuery)
	if err != nil {
		return "", err
	}

	host := u.Hostname()
	if host == "" {
		return "", errors.ErrInvalidSRTURL
	}
	if netguard.CheckHost(host) != nil {
		return "", errors.ErrSRTHostNotAllowed
	}

	lookupCtx, cancel := context.WithTimeout(ctx, srtLookupTimeout)
	defer cancel()
	addrs, err := lookupNetIP(lookupCtx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return "", errors.ErrSRTHostUnresolved
	}
	for _, addr := range addrs {
		if netguard.IsBlockedIP(addr.AsSlice()) {
			return "", errors.ErrSRTHostNotAllowed
		}
	}

	pinned := addrs[0].Unmap().WithZone("").String()
	if port := u.Port(); port != "" {
		pinned = net.JoinHostPort(pinned, port)
	} else if strings.Contains(pinned, ":") {
		pinned = "[" + pinned + "]"
	}

	out := "srt://" + pinned
	if len(query) > 0 {
		out += "?" + strings.Join(query, "&")
	}
	return out, nil
}

func parseSRTQuery(rawQuery string) ([]string, error) {
	var query []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		rawKey, rawValue, ok := strings.Cut(part, "=")
		if !ok {
			return nil, errors.ErrInvalidSRTURL
		}
		key, err := url.PathUnescape(rawKey)
		if err != nil {
			return nil, errors.ErrInvalidSRTURL
		}
		value, err := url.PathUnescape(rawValue)
		if err != nil || strings.ContainsRune(value, 0) {
			return nil, errors.ErrInvalidSRTURL
		}
		if !allowedSRTOptions[key] || seen[key] || (key == "mode" && value != "caller") {
			return nil, errors.ErrInvalidSRTURL
		}
		seen[key] = true
		query = append(query, key+"="+escapeSRTQueryValue(value))
	}
	return query, nil
}

func escapeSRTQueryValue(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
