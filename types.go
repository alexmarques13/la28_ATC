package main

import "encoding/json"

type QueueConfig struct {
	CustomerID               string
	EventID                  string
	LayoutName               string
	LayoutVersion            int
	TargetURL                string
	EnqueueToken             string
	ChallengeChecksum        string
	ChallengeVerifyEndpoint  string
	ChallengesIssuedByReason int
	Culture                  string
	QueueURL                 string
}

type QueueStatus struct {
	QueueID             string
	QueueNumber         *int
	UsersAhead          *int
	ExpectedServiceTime string
	QueuePaused         bool
	Progress            *float64
	PageID              string
	RedirectURL         string
	Raw                 json.RawMessage
}

type LoginResult struct {
	UID      string          `json:"uid"`
	Email    string          `json:"email"`
	EsID     string          `json:"esId"`
	SSO      json.RawMessage `json:"sso"`
	LoggedIn bool            `json:"logged_in"`
	FinalURL string          `json:"final_url"`
}

type RunResult struct {
	Queue  *QueueStatus
	Login  *LoginResult
	Basket *BasketResult
}
