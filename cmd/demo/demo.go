package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// TokenAuthSystem 包含加密密钥
type TokenAuthSystem struct {
	key []byte
}

// NewTokenAuthSystem 创建一个新的 TokenAuthSystem 实例
func NewTokenAuthSystem(key []byte) *TokenAuthSystem {
	return &TokenAuthSystem{key: key}
}

// PKCS7Padding 添加PKCS7填充
func PKCS7Padding(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

// PKCS7UnPadding 移除PKCS7填充
func PKCS7UnPadding(data []byte) []byte {
	padding := data[len(data)-1]
	return data[:len(data)-int(padding)]
}

// GenerateToken 生成加密后的 token
func (tas *TokenAuthSystem) GenerateToken(data string) (string, error) {
	block, err := aes.NewCipher(tas.key)
	if err != nil {
		return "", err
	}

	// 添加PKCS7填充
	dataBytes := PKCS7Padding([]byte(data), aes.BlockSize)

	cipherText := make([]byte, aes.BlockSize+len(dataBytes))
	iv := cipherText[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText[aes.BlockSize:], dataBytes)

	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// VerifyToken 验证并解密 token
func (tas *TokenAuthSystem) VerifyToken(token string) (string, error) {
	block, err := aes.NewCipher(tas.key)
	if err != nil {
		return "", err
	}

	cipherText, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}

	if len(cipherText) < aes.BlockSize {
		return "", fmt.Errorf("invalid token")
	}

	iv := cipherText[:aes.BlockSize]
	cipherText = cipherText[aes.BlockSize:]

	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(cipherText, cipherText)

	// 移除PKCS7填充
	decryptedData := PKCS7UnPadding(cipherText)

	return string(decryptedData), nil
}

func main() {
	// 替换为你自己的密钥
	key := []byte("32-byte-secret-key-1234567890123")

	// 创建 TokenAuthSystem 实例
	tokenAuthSystem := NewTokenAuthSystem(key)

	// 生成一个 token
	dataToEncrypt := "user_id:12345"
	token, err := tokenAuthSystem.GenerateToken(dataToEncrypt)
	if err != nil {
		fmt.Println("Token generation failed:", err)
		return
	}
	fmt.Println("Generated Token:", token)

	// 验证 token
	decryptedData, err := tokenAuthSystem.VerifyToken(token)
	if err != nil {
		fmt.Println("Token verification failed:", err)
		return
	}
	fmt.Println("Decrypted Data:", decryptedData)
}
