package auth

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	defaultBcryptCost   = 10
	minLocalPasswordLen = 8
	localProviderName   = "local"
)

// HashPassword bcrypt 哈希密码。
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), defaultBcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword 校验明文与 bcrypt 哈希。
func CheckPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ValidateLocalPassword 校验本地密码策略。
func ValidateLocalPassword(password string) error {
	if utf8.RuneCountInString(password) < minLocalPasswordLen {
		return fmt.Errorf("密码长度至少 %d 位", minLocalPasswordLen)
	}
	return nil
}

// NormalizeLocalLogin 规范化本地登录名（邮箱/用户名）。
func NormalizeLocalLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}
