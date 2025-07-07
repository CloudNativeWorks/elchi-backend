package middleware

import (
	"fmt"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func GinLog(logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		start := time.Now()

		c.Next()

		stop := time.Since(start)
		responseTime := float32(stop.Nanoseconds()) / 1000000.0
		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		clientUserAgent := c.Request.UserAgent()
		referer := c.Request.Referer()
		requestURI := c.Request.RequestURI

		matchKubeProbeUserAgent, _ := regexp.MatchString("^(kube-probe.*)$", clientUserAgent)

		if clientUserAgent == "Envoy/HC" || clientUserAgent == "Consul Health Check" || matchKubeProbeUserAgent {
			return
		}

		fields := logrus.Fields{
			"statusCode":     statusCode,
			"responseTime":   fmt.Sprintf("%.2fms", responseTime),
			"clientIP":       clientIP,
			"requestMethod":  c.Request.Method,
			"requestPath":    path,
			"requestReferer": referer,
			"userAgent":      clientUserAgent,
			"requestUri":     requestURI,
		}

		if len(c.Errors) > 0 {
			err := c.Errors.ByType(gin.ErrorTypePrivate).String()
			logger.WithFields(fields).Error(err)
		} else {
			switch {
			case statusCode > 499:
				logger.WithFields(fields).Errorf("HTTP Status Failed")
			case statusCode > 399:
				logger.WithFields(fields).Warnf("HTTP Status Failed")
			default:
				logger.WithFields(fields).Infof("HTTP Status OK")
			}
		}
	}
}
