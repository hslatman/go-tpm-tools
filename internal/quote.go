package internal

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/subtle"
	"fmt"

	pb "github.com/google/go-tpm-tools/proto"
	"github.com/google/go-tpm/tpm2"
)

// VerifyQuote performs the following checks to validate a Quote:
//    - the provided signature is generated by the trusted AK public key
//    - the signature signs the provided quote data
//    - the quote data starts with TPM_GENERATED_VALUE
//    - the quote data is a valid TPMS_QUOTE_INFO
//    - the quote data was taken over the provided PCRs
//    - the provided PCR values match the quote data internal digest
//    - the provided extraData matches that in the quote data
// Note that the caller must have already established trust in the provided
// public key before validating the Quote.
//
// VerifyQuote supports ECDSA and RSASSA signature verification.
func VerifyQuote(q *pb.Quote, trustedPub crypto.PublicKey, extraData []byte) error {
	sig, err := tpm2.DecodeSignature(bytes.NewBuffer(q.GetRawSig()))
	if err != nil {
		return fmt.Errorf("signature decoding failed: %v", err)
	}

	var hash crypto.Hash
	switch pub := trustedPub.(type) {
	case *ecdsa.PublicKey:
		hash, err = sig.ECC.HashAlg.Hash()
		if err != nil {
			return err
		}
		if err = verifyECDSAQuoteSignature(pub, hash, q.GetQuote(), sig); err != nil {
			return err
		}
	case *rsa.PublicKey:
		hash, err = sig.RSA.HashAlg.Hash()
		if err != nil {
			return err
		}
		if err = verifyRSASSAQuoteSignature(pub, hash, q.GetQuote(), sig); err != nil {
			return err
		}
	default:
		return fmt.Errorf("only RSA and ECC public keys are currently supported, received type: %T", pub)

	}

	// Decode and check for magic TPMS_GENERATED_VALUE.
	attestationData, err := tpm2.DecodeAttestationData(q.GetQuote())
	if err != nil {
		return fmt.Errorf("decoding attestation data failed: %v", err)
	}
	if attestationData.Type != tpm2.TagAttestQuote {
		return fmt.Errorf("expected quote tag, got: %v", attestationData.Type)
	}
	attestedQuoteInfo := attestationData.AttestedQuoteInfo
	if attestedQuoteInfo == nil {
		return fmt.Errorf("attestation data does not contain quote info")
	}
	if subtle.ConstantTimeCompare(attestationData.ExtraData, extraData) == 0 {
		return fmt.Errorf("quote extraData did not match expected extraData")
	}
	return validatePCRDigest(attestedQuoteInfo, q.GetPcrs(), hash)
}

func verifyECDSAQuoteSignature(ecdsaPub *ecdsa.PublicKey, hash crypto.Hash, quoted []byte, sig *tpm2.Signature) error {
	if sig.Alg != tpm2.AlgECDSA {
		return fmt.Errorf("signature scheme 0x%x is not supported, only ECDSA is supported", sig.Alg)
	}

	hashConstructor := hash.New()
	hashConstructor.Write(quoted)
	if !ecdsa.Verify(ecdsaPub, hashConstructor.Sum(nil), sig.ECC.R, sig.ECC.S) {
		return fmt.Errorf("ECC signature verification failed")
	}
	return nil
}

func verifyRSASSAQuoteSignature(rsaPub *rsa.PublicKey, hash crypto.Hash, quoted []byte, sig *tpm2.Signature) error {
	if sig.Alg != tpm2.AlgRSASSA {
		return fmt.Errorf("signature scheme 0x%x is not supported, only RSASSA (PKCS#1 v1.5) is supported", sig.Alg)
	}

	hashConstructor := hash.New()
	hashConstructor.Write(quoted)
	if err := rsa.VerifyPKCS1v15(rsaPub, hash, hashConstructor.Sum(nil), sig.RSA.Signature); err != nil {
		return fmt.Errorf("RSASSA signature verification failed: %v", err)
	}
	return nil
}

func validatePCRDigest(quoteInfo *tpm2.QuoteInfo, pcrs *pb.Pcrs, hash crypto.Hash) error {
	if !SamePCRSelection(pcrs, quoteInfo.PCRSelection) {
		return fmt.Errorf("given PCRs and Quote do not have the same PCR selection")
	}
	pcrDigest := PCRDigest(pcrs, hash)
	if subtle.ConstantTimeCompare(quoteInfo.PCRDigest, pcrDigest) == 0 {
		return fmt.Errorf("given PCRs digest not matching")
	}
	return nil
}
