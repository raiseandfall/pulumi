// Copyright 2016-2017, Pulumi Corporation.  All rights reserved.

package cmd

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/pulumi/pulumi/pkg/pack"
	"github.com/pulumi/pulumi/pkg/resource/config"
	"golang.org/x/crypto/pbkdf2"

	"github.com/pkg/errors"

	"github.com/pulumi/pulumi/pkg/diag"
	"github.com/pulumi/pulumi/pkg/diag/colors"
	"github.com/pulumi/pulumi/pkg/engine"
	"github.com/pulumi/pulumi/pkg/tokens"
	"github.com/pulumi/pulumi/pkg/util/contract"
	"github.com/pulumi/pulumi/pkg/workspace"
)

// newWorkspace creates a new workspace using the current working directory.
func newWorkspace() (workspace.W, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return workspace.New(pwd)
}

// explicitOrCurrent returns an stack name after ensuring the stack exists. When a empty
// stack name is passed, the "current" ambient stack is returned
func explicitOrCurrent(name string) (tokens.QName, error) {
	if name == "" {
		return getCurrentStack()
	}

	_, _, _, err := getStack(tokens.QName(name))
	return tokens.QName(name), err
}

// getCurrentStack reads the current stack.
func getCurrentStack() (tokens.QName, error) {
	w, err := newWorkspace()
	if err != nil {
		return "", err
	}

	stack := w.Settings().Stack
	if stack == "" {
		return "", errors.New("no current stack detected; please use `pulumi stack init` to create one")
	}
	return stack, nil
}

// setCurrentStack changes the current stack to the given stack name, issuing an error if it doesn't exist.
func setCurrentStack(name tokens.QName, verify bool) error {
	if verify {
		if _, _, _, err := getStack(name); err != nil {
			return err
		}
	}

	// Switch the current workspace to that stack.
	w, err := newWorkspace()
	if err != nil {
		return err
	}

	w.Settings().Stack = name
	return w.Save()
}

// displayEvents reads events from the `events` channel until it is closed, displaying each event as it comes in.
// Once all events have been read from the channel and displayed, it closes the `done` channel so the caller can
// await all the events being written.
func displayEvents(events <-chan engine.Event, done chan bool, debug bool) {
	defer func() {
		done <- true
	}()

Outer:
	for event := range events {
		switch event.Type {
		case engine.CancelEvent:
			break Outer
		case engine.StdoutColorEvent:
			fmt.Print(colors.ColorizeText(event.Payload.(string)))
		case engine.DiagEvent:
			payload := event.Payload.(engine.DiagEventPayload)
			var out io.Writer
			out = os.Stdout

			if payload.Severity == diag.Error || payload.Severity == diag.Warning {
				out = os.Stderr
			}
			if payload.Severity == diag.Debug && !debug {
				out = ioutil.Discard
			}
			msg := payload.Message
			if payload.UseColor {
				msg = colors.ColorizeText(msg)
			}
			fmt.Fprintf(out, msg)
		default:
			contract.Failf("unknown event type '%s'", event.Type)
		}
	}
}

func getPackage() (*pack.Package, error) {
	pkgPath, err := getPackageFilePath()
	if err != nil {
		return nil, err
	}

	pkg, err := pack.Load(pkgPath)

	return pkg, err
}

func savePackage(pkg *pack.Package) error {
	pkgPath, err := getPackageFilePath()
	if err != nil {
		return err
	}

	return pack.Save(pkgPath, pkg)
}

func getPackageFilePath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	pkgPath, err := workspace.DetectPackage(dir)
	if err != nil {
		return "", err
	}

	if pkgPath == "" {
		return "", errors.Errorf("could not find Pulumi.yaml (searching upwards from %s)", dir)
	}

	return pkgPath, nil
}

func readConsoleNoEchoWithPrompt(prompt string) (string, error) {
	if prompt != "" {
		fmt.Printf("%s: ", prompt)
	}

	return readConsoleNoEcho()
}

func readPassPhrase(prompt string) (string, error) {
	if phrase, has := os.LookupEnv("PULUMI_CONFIG_PASSPHRASE"); has {
		return phrase, nil
	}

	return readConsoleNoEchoWithPrompt(prompt)
}

func getSymmetricCrypter() (config.ValueEncrypterDecrypter, error) {
	pkg, err := getPackage()
	if err != nil {
		return nil, err
	}

	return getSymmetricCrypterForPackage(pkg)
}

func getSymmetricCrypterForPackage(pkg *pack.Package) (config.ValueEncrypterDecrypter, error) {
	if pkg.EncryptionSalt != "" {
		phrase, err := readPassPhrase("passphrase")
		if err != nil {
			return nil, err
		}

		return symmetricCrypterFromPhraseAndState(phrase, pkg.EncryptionSalt)
	}

	phrase, err := readPassPhrase("passphrase")
	if err != nil {
		return nil, err

	}

	confirm, err := readPassPhrase("passphrase (confirm)")
	if err != nil {
		return nil, err
	}

	if phrase != confirm {
		return nil, errors.New("passphrases do not match")
	}

	salt := make([]byte, 8)

	_, err = cryptorand.Read(salt)
	contract.Assertf(err == nil, "could not read from system random")

	c := symmetricCrypter{key: keyFromPassPhrase(phrase, salt, aes256GCMKeyBytes)}

	// Encrypt a message and store it with the salt so we can test if the password is correct later
	msg, err := c.EncryptValue("pulumi")
	contract.Assert(err == nil)

	pkg.EncryptionSalt = fmt.Sprintf("v1:%s:%s", base64.StdEncoding.EncodeToString(salt), msg)

	return c, nil
}

func keyFromPassPhrase(phrase string, salt []byte, keyLength int) []byte {
	// 1,000,000 iterations was chosen because it took a little over a second on an i7-7700HQ Quad Core procesor
	return pbkdf2.Key([]byte(phrase), salt, 1000000, keyLength, sha256.New)
}

func hasSecureValue(config map[tokens.ModuleMember]config.Value) bool {
	for _, v := range config {
		if v.Secure() {
			return true
		}
	}

	return false
}