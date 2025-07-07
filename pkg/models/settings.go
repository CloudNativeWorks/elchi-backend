package models

type Settings struct {
	Tokens []Token `bson:"tokens"`
}

type Token struct {
	Token string `bson:"token"`
	Name  string `bson:"name"`
}