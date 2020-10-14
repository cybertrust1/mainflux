package dtls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"math/big"
	"time"
)

type ecdsaSignature struct {
	R, S *big.Int
}

func valueKeyMessage(clientRandom, serverRandom, publicKey []byte, namedCurve namedCurve) []byte {
	serverECDHParams := make([]byte, 4)
	serverECDHParams[0] = 3 // named curve
	binary.BigEndian.PutUint16(serverECDHParams[1:], uint16(namedCurve))
	serverECDHParams[3] = byte(len(publicKey))

	plaintext := []byte{}
	plaintext = append(plaintext, clientRandom...)
	plaintext = append(plaintext, serverRandom...)
	plaintext = append(plaintext, serverECDHParams...)
	plaintext = append(plaintext, publicKey...)

	return plaintext
}

// If the client provided a "signature_algorithms" extension, then all
// certificates provided by the server MUST be signed by a
// hash/signature algorithm pair that appears in that extension
//
// https://tools.ietf.org/html/rfc5246#section-7.4.2
func generateKeySignature(clientRandom, serverRandom, publicKey []byte, namedCurve namedCurve, privateKey crypto.PrivateKey, hashAlgorithm hashAlgorithm) ([]byte, error) {
	msg := valueKeyMessage(clientRandom, serverRandom, publicKey, namedCurve)
	switch p := privateKey.(type) {
	case ed25519.PrivateKey:
		// https://crypto.stackexchange.com/a/55483
		return p.Sign(rand.Reader, msg, crypto.Hash(0))
	case *ecdsa.PrivateKey:
		hashed := hashAlgorithm.digest(msg)
		return p.Sign(rand.Reader, hashed, hashAlgorithm.cryptoHash())
	case *rsa.PrivateKey:
		hashed := hashAlgorithm.digest(msg)
		return p.Sign(rand.Reader, hashed, hashAlgorithm.cryptoHash())
	}

	return nil, errKeySignatureGenerateUnimplemented
}

func verifyKeySignature(message, remoteKeySignature []byte, hashAlgorithm hashAlgorithm, rawCertificates [][]byte) error {
	if len(rawCertificates) == 0 {
		return errLengthMismatch
	}
	certificate, err := x509.ParseCertificate(rawCertificates[0])
	if err != nil {
		return err
	}

	switch p := certificate.PublicKey.(type) {
	case ed25519.PublicKey:
		if ok := ed25519.Verify(p, message, remoteKeySignature); !ok {
			return errKeySignatureMismatch
		}
		return nil
	case *ecdsa.PublicKey:
		ecdsaSig := &ecdsaSignature{}
		if _, err := asn1.Unmarshal(remoteKeySignature, ecdsaSig); err != nil {
			return err
		}
		if ecdsaSig.R.Sign() <= 0 || ecdsaSig.S.Sign() <= 0 {
			return errInvalidECDSASignature
		}
		hashed := hashAlgorithm.digest(message)
		if !ecdsa.Verify(p, hashed, ecdsaSig.R, ecdsaSig.S) {
			return errKeySignatureMismatch
		}
		return nil
	case *rsa.PublicKey:
		switch certificate.SignatureAlgorithm {
		case x509.SHA1WithRSA, x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA:
			hashed := hashAlgorithm.digest(message)
			return rsa.VerifyPKCS1v15(p, hashAlgorithm.cryptoHash(), hashed, remoteKeySignature)
		}
	}

	return errKeySignatureVerifyUnimplemented
}

// If the server has sent a CertificateRequest message, the client MUST send the Certificate
// message.  The ClientKeyExchange message is now sent, and the content
// of that message will depend on the public key algorithm selected
// between the ClientHello and the ServerHello.  If the client has sent
// a certificate with signing ability, a digitally-signed
// CertificateVerify message is sent to explicitly verify possession of
// the private key in the certificate.
// https://tools.ietf.org/html/rfc5246#section-7.3
func generateCertificateVerify(handshakeBodies []byte, privateKey crypto.PrivateKey, hashAlgorithm hashAlgorithm) ([]byte, error) {
	h := sha256.New()
	if _, err := h.Write(handshakeBodies); err != nil {
		return nil, err
	}
	hashed := h.Sum(nil)

	switch p := privateKey.(type) {
	case ed25519.PrivateKey:
		// https://crypto.stackexchange.com/a/55483
		return p.Sign(rand.Reader, hashed, crypto.Hash(0))
	case *ecdsa.PrivateKey:
		return p.Sign(rand.Reader, hashed, hashAlgorithm.cryptoHash())
	case *rsa.PrivateKey:
		return p.Sign(rand.Reader, hashed, hashAlgorithm.cryptoHash())
	}

	return nil, errInvalidSignatureAlgorithm
}

func verifyCertificateVerify(handshakeBodies []byte, hashAlgorithm hashAlgorithm, remoteKeySignature []byte, rawCertificates [][]byte) error {
	if len(rawCertificates) == 0 {
		return errLengthMismatch
	}
	certificate, err := x509.ParseCertificate(rawCertificates[0])
	if err != nil {
		return err
	}

	switch p := certificate.PublicKey.(type) {
	case ed25519.PublicKey:
		if ok := ed25519.Verify(p, handshakeBodies, remoteKeySignature); !ok {
			return errKeySignatureMismatch
		}
		return nil
	case *ecdsa.PublicKey:
		ecdsaSig := &ecdsaSignature{}
		if _, err := asn1.Unmarshal(remoteKeySignature, ecdsaSig); err != nil {
			return err
		}
		if ecdsaSig.R.Sign() <= 0 || ecdsaSig.S.Sign() <= 0 {
			return errInvalidECDSASignature
		}
		hash := hashAlgorithm.digest(handshakeBodies)
		if !ecdsa.Verify(p, hash, ecdsaSig.R, ecdsaSig.S) {
			return errKeySignatureMismatch
		}
		return nil
	case *rsa.PublicKey:
		switch certificate.SignatureAlgorithm {
		case x509.SHA1WithRSA, x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA:
			hash := hashAlgorithm.digest(handshakeBodies)
			return rsa.VerifyPKCS1v15(p, hashAlgorithm.cryptoHash(), hash, remoteKeySignature)
		}
	}

	return errKeySignatureVerifyUnimplemented
}

func loadCerts(rawCertificates [][]byte) ([]*x509.Certificate, error) {
	if len(rawCertificates) == 0 {
		return nil, errLengthMismatch
	}

	certs := make([]*x509.Certificate, 0, len(rawCertificates))
	for _, rawCert := range rawCertificates {
		cert, err := x509.ParseCertificate(rawCert)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

func verifyClientCert(rawCertificates [][]byte, roots *x509.CertPool) (chains [][]*x509.Certificate, err error) {
	certificate, err := loadCerts(rawCertificates)
	if err != nil {
		return nil, err
	}
	intermediateCAPool := x509.NewCertPool()
	for _, cert := range certificate[1:] {
		intermediateCAPool.AddCert(cert)
	}
	opts := x509.VerifyOptions{
		Roots:         roots,
		CurrentTime:   time.Now(),
		Intermediates: intermediateCAPool,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return certificate[0].Verify(opts)
}

func verifyServerCert(rawCertificates [][]byte, roots *x509.CertPool, serverName string) (chains [][]*x509.Certificate, err error) {
	certificate, err := loadCerts(rawCertificates)
	if err != nil {
		return nil, err
	}
	intermediateCAPool := x509.NewCertPool()
	for _, cert := range certificate[1:] {
		intermediateCAPool.AddCert(cert)
	}
	opts := x509.VerifyOptions{
		Roots:         roots,
		CurrentTime:   time.Now(),
		DNSName:       serverName,
		Intermediates: intermediateCAPool,
	}
	return certificate[0].Verify(opts)
}

func generateAEADAdditionalData(h *recordLayerHeader, payloadLen int) []byte {
	var additionalData [13]byte
	// SequenceNumber MUST be set first
	// we only want uint48, clobbering an extra 2 (using uint64, Golang doesn't have uint48)
	binary.BigEndian.PutUint64(additionalData[:], h.sequenceNumber)
	binary.BigEndian.PutUint16(additionalData[:], h.epoch)
	additionalData[8] = byte(h.contentType)
	additionalData[9] = h.protocolVersion.major
	additionalData[10] = h.protocolVersion.minor
	binary.BigEndian.PutUint16(additionalData[len(additionalData)-2:], uint16(payloadLen))

	return additionalData[:]
}