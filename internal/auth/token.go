package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
)

func FetchJWTToken(clientID, clientSecret string) string {
	url := "https://www.deribit.com/api/v2/public/auth"

	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "public/auth",
		"params": map[string]string{
			"grant_type":    "client_credentials",
			"client_id":     clientID,
			"client_secret": clientSecret,
		},
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal("[AUTH] Token request failed:", err)
	}
	defer res.Body.Close()

	// ✅ 디버깅 로그: StatusCode
	// log.Println("[DEBUG] Auth Status Code:", res.StatusCode)

	// ✅ 디버깅 로그: Body 전체 출력 (민감값은 서버 응답이므로 client_secret 노출 없음)
	rawBody, _ := io.ReadAll(res.Body)
	// log.Println("[DEBUG] Auth Raw Response:", string(rawBody))

	// ✅ 다시 decode 위해 buffer 사용
	var r struct {
		Result struct {
			AccessToken string `json:"access_token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawBody, &r); err != nil {
		log.Fatal("[AUTH] Decode failed:", err)
	}

	// ✅ 토큰 값만 로그 (디버깅)
	// log.Println("[DEBUG] Access Token:", r.Result.AccessToken)

	return r.Result.AccessToken
}
