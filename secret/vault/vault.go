package vault

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"github.com/hashicorp/vault/api"
	"software.sslmate.com/src/go-pkcs12"
)

// Vault represents Hashicorp Vault
type Vault struct {
	client *api.Client
}

// New constructs a new Vault
func New() (*Vault, error) {
	cfg := api.DefaultConfig()
	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	return &Vault{client: client}, nil
}

// GetSecrets returns all available data as key value for given path
// it extracts cert and private key from pkcs12 data
func (v *Vault) GetSecrets(path string) (map[string][]byte, error) {
	secrets, err := v.client.Logical().Read(path)
	if err != nil {
		return nil, err
	}

	if secrets == nil {
		return nil, fmt.Errorf("path %s not exist", path)
	}

	if isExist(secrets.Data, "pkcs12") {
		return getKeyPairPKCS12PEM(secrets.Data)
	}

	return getSecrets(secrets.Data), nil
}

// getKeyPairPKCS12PEM returns certificate from pkcs12 archive
// private key and X.509 certificate encoded as PEM
// pkcs12=pkcs12_data password=password
// password is optional
func getKeyPairPKCS12PEM(data map[string]interface{}) (map[string][]byte, error) {
	var (
		password = ""
		secrets  = make(map[string][]byte)
	)

	b, err := base64.StdEncoding.DecodeString(data["pkcs12"].(string))
	if err != nil {
		return nil, err
	}

	if isExist(data, "password") {
		password = data["password"].(string)
	}

	key, cert, err := pkcs12.Decode(b, password)
	if err != nil {
		return nil, err
	}

	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}

	keyPEM := &bytes.Buffer{}
	err = pem.Encode(keyPEM, &pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})
	if err != nil {
		return nil, err
	}

	certPEM := &bytes.Buffer{}
	err = pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err != nil {
		return nil, err
	}

	secrets["cert"] = certPEM.Bytes()
	secrets["key"] = keyPEM.Bytes()

	return secrets, nil
}

// getSecrets returns private key and certificate encoded as PEM
func getSecrets(data map[string]interface{}) map[string][]byte {
	var result = make(map[string][]byte)

	for key, value := range data {
		result[key] = []byte(value.(string))
	}

	return result
}

func isExist(m map[string]interface{}, k string) bool {
	_, ok := m[k]
	return ok
}