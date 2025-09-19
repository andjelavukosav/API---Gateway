package util

import (
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var jwtKey = []byte("tajna_lozinka") // Mora da bude isto kao i u stakeholders servisu

type Claims struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"http://schemas.microsoft.com/ws/2008/06/identity/claims/role"`
	jwt.RegisteredClaims
}

// ExtractTokenFromHeader uzima token iz Authorization headera
func ExtractTokenFromHeader(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", errors.New("missing Authorization header")
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", errors.New("invalid Authorization header format")
	}

	return parts[1], nil
}

// ParseToken provjerava validnost tokena i vraća Claims
func ParseToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
		return jwtKey, nil
	})

	if err != nil || !token.Valid {
		return nil, errors.New("invalid or expired token")
	}

	return claims, nil
}

// ExtractUserIDFromToken je javna funkcija koja vraća userID iz JWT tokena
func ExtractUserIDFromToken(r *http.Request) (string, error) {
	tokenStr, err := ExtractTokenFromHeader(r)
	if err != nil {
		return "", err
	}

	claims, err := ParseToken(tokenStr)
	if err != nil {
		return "", err
	}

	return claims.ID, nil
}
