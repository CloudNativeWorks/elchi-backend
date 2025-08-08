package models

type Settings struct {
	Project     string `bson:"project"`
	Tokens      []Token `bson:"tokens"`
	ClaudeToken string  `bson:"claude_token,omitempty"`
}

type Token struct {
	Token string `bson:"token"`
	Name  string `bson:"name"`
	ID    string `bson:"id"`
}