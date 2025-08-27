package models

type Settings struct {
	Project          string `bson:"project"`
	Tokens           []Token `bson:"tokens"`
	OpenRouterToken  string  `bson:"openrouter_token,omitempty"`
	AIDefaultModel   string  `bson:"ai_default_model,omitempty"`
	DiscoveryToken   string  `bson:"discovery_token,omitempty"`
}

type Token struct {
	Token string `bson:"token"`
	Name  string `bson:"name"`
	ID    string `bson:"id"`
}