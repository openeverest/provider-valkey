// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"crypto/rand"
	"fmt"
	"math/big"

	corev1 "k8s.io/api/core/v1"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const (
	// authSecretSuffix is appended to the Instance name to form the Secret that
	// holds the generated password for the default user.
	authSecretSuffix = "-auth"

	// defaultUsername is the built-in Valkey user that clients authenticate as.
	defaultUsername = "default"

	// authSecretKeyPassword is the key under which the default user's password
	// is stored in the auth Secret. The valkey-operator reads it via the user's
	// passwordSecret reference.
	authSecretKeyPassword = "default"

	// defaultUserACL grants the default user unrestricted access, matching
	// Valkey's out-of-the-box default user permissions while requiring a
	// password.
	defaultUserACL = "+@all ~* &*"

	// passwordLength is the number of characters in the generated password.
	passwordLength = 32
)

// passwordCharset is the alphabet used for generated passwords. It avoids URI
// reserved characters so the password can be embedded in a connection URI
// without escaping.
const passwordCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// authSecretName returns the name of the auth Secret for the given instance.
func authSecretName(instance string) string {
	return instance + authSecretSuffix
}

// ensureAuthSecret creates the Secret holding the default user's password when
// it does not already exist. An existing Secret is left untouched so the
// password stays stable across reconciles.
func ensureAuthSecret(c *controller.Context) error {
	name := authSecretName(c.Name())

	exists, err := c.Exists(&corev1.Secret{}, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	password, err := generatePassword(passwordLength)
	if err != nil {
		return fmt.Errorf("generating password: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(name),
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			authSecretKeyPassword: []byte(password),
		},
	}
	return c.Apply(secret)
}

// readDefaultPassword returns the default user's password from the instance's
// auth Secret.
func readDefaultPassword(c *controller.Context) (string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, authSecretName(c.Name())); err != nil {
		return "", err
	}
	password, ok := secret.Data[authSecretKeyPassword]
	if !ok || len(password) == 0 {
		return "", fmt.Errorf("auth secret %q is missing %q", authSecretName(c.Name()), authSecretKeyPassword)
	}
	return string(password), nil
}

// generatePassword returns a cryptographically random password of the given
// length drawn from passwordCharset.
func generatePassword(length int) (string, error) {
	buf := make([]byte, length)
	max := big.NewInt(int64(len(passwordCharset)))
	for i := range buf {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		buf[i] = passwordCharset[n.Int64()]
	}
	return string(buf), nil
}
