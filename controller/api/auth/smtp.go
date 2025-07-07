package auth

import (
	"fmt"
	"net/smtp"
)

func SendEmail(userName, passwd, mail, smtpPassword string) error {
	smtpHost := "smtp-relay.brevo.com"
	smtpPort := "587"
	email := "82a0d8001@smtp-brevo.com"
	to := []string{mail}
	subject := "Subject: Welcome to Elchi - Your Demo Account is Ready!\n"
	from := "demo@elchi.io"

	body := fmt.Sprintf(
		"Hello,\n\nYour demo Elchi account has been successfully created. You can log in using the credentials below:\n\n"+
			"- Username: %s\n"+
			"- Password: %s\n\n"+
			"Please note: This account will remain valid for 24 hours. After that, it will be automatically deleted.\n\n"+
			"Best regards",
		userName, passwd,
	)

	message := []byte("From: " + from + "\n" +
		"To: " + to[0] + "\n" +
		subject +
		"\n" +
		body)

	auth := smtp.PlainAuth("", email, smtpPassword, smtpHost)
	err := smtp.SendMail(smtpHost+":"+smtpPort, auth, from, to, message)
	if err != nil {
		fmt.Println("email send err:", err)
		return err
	}

	return nil
}
