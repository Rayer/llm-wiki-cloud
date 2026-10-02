// Command demo_password_rotate_once is a target-locked, one-off password
// rotation tool for the existing Demo identity. It is dry-run unless --apply
// is supplied. Apply reads exact password bytes from --password-fd; no password
// value is accepted as a flag or environment variable.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	devProjectID      = "llm-wiki-cloud"
	devDatabaseID     = "llm-wiki-cloud-dev"
	devDemoUserID     = "e492f6bdaf1735e12b2de96d"
	emulatorProjectID = "lwc-366-password-rotation-fixture"
	emulatorDatabase  = "lwc-366-password-rotation-fixture"
	emulatorUserID    = "fixture-demo-user"
	maxPasswordBytes  = 72 // bcrypt's existing maximum input length.
	operationTimeout  = 30 * time.Second
)

var (
	errArguments        = errors.New("invalid command arguments")
	errTargetRejected   = errors.New("target configuration rejected")
	errPasswordInput    = errors.New("password input rejected")
	errPasswordInvalid  = errors.New("password input is invalid")
	errUserMissing      = errors.New("target user not found")
	errUserInactive     = errors.New("target user is not active")
	errNoPasswordLogin  = errors.New("target user has no valid password login")
	errPasswordNoop     = errors.New("replacement password matches the current password")
	errIdentityInvalid  = errors.New("target identity is invalid")
	errStoreUnavailable = errors.New("identity store unavailable")
	errRotationConflict = errors.New("identity changed during preflight")
	errRotationUnknown  = errors.New("rotation outcome unknown; inspect state before any retry")
)

type target struct {
	project      string
	database     string
	userID       string
	emulatorHost string
}

type options struct {
	target     target
	apply      bool
	passwordFD int
}

type rotationSnapshot struct {
	user auth.UserRecord
	data map[string]interface{}
}

type rotationStore interface {
	readUser(context.Context, string) (rotationSnapshot, error)
	updatePasswordHash(context.Context, string, rotationSnapshot, string) error
}

type storeFactory func(context.Context, target) (rotationStore, func(), error)
type passwordFDOpener func(int) (io.ReadCloser, error)

type rotationReceipt struct {
	Project      string `json:"project"`
	Database     string `json:"database"`
	UserID       string `json:"user_id"`
	Action       string `json:"action"`
	Outcome      string `json:"outcome"`
	ResultHandle string `json:"result_handle"`
}

func main() {
	if err := run(os.Args[1:], openPasswordFD, newFirestoreStore, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, openPassword passwordFDOpener, makeStore storeFactory, output io.Writer) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	if err := validateTarget(options.target); err != nil {
		return err
	}
	if openPassword == nil || makeStore == nil || output == nil {
		return errArguments
	}

	var password []byte
	if options.apply {
		input, err := openPassword(options.passwordFD)
		if err != nil {
			return errPasswordInput
		}
		password, err = readPassword(input)
		_ = input.Close()
		if err != nil || !validPassword(password) {
			wipe(password)
			return errPasswordInvalid
		}
		defer wipe(password)
	}

	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	store, closeStore, err := makeStore(ctx, options.target)
	if err != nil || store == nil {
		return errStoreUnavailable
	}
	if closeStore != nil {
		defer closeStore()
	}

	receipt, operationErr := rotate(ctx, options.target, store, options.apply, password)
	if receipt.Outcome != "" {
		if err := json.NewEncoder(output).Encode(receipt); err != nil {
			return errors.New("unable to write rotation receipt")
		}
	}
	return operationErr
}

func parseOptions(args []string) (options, error) {
	var result options
	set := flag.NewFlagSet("demo_password_rotate_once", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&result.target.project, "project", "", "explicit GCP project ID")
	set.StringVar(&result.target.database, "database", "", "explicit Firestore database ID")
	set.StringVar(&result.target.userID, "user-id", "", "existing Demo user document ID")
	set.StringVar(&result.target.emulatorHost, "emulator-host", "", "explicit loopback emulator host:port for synthetic fixtures")
	set.BoolVar(&result.apply, "apply", false, "replace the existing password hash; default is read-only dry-run")
	set.IntVar(&result.passwordFD, "password-fd", -1, "file descriptor containing exact replacement password bytes (apply only)")
	if set.Parse(args) != nil || set.NArg() != 0 {
		return options{}, errArguments
	}
	if result.apply != (result.passwordFD >= 0) {
		return options{}, errArguments
	}
	if !result.apply && result.passwordFD >= 0 {
		return options{}, errArguments
	}
	return result, nil
}

func validateTarget(t target) error {
	if strings.TrimSpace(t.project) == "" || strings.TrimSpace(t.database) == "" || strings.TrimSpace(t.userID) == "" {
		return errTargetRejected
	}
	if t.emulatorHost != "" {
		if err := validateLoopbackHost(t.emulatorHost); err != nil {
			return errTargetRejected
		}
		if t.project != emulatorProjectID || t.database != emulatorDatabase || t.userID != emulatorUserID {
			return errTargetRejected
		}
		if inherited := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")); inherited != "" && inherited != t.emulatorHost {
			return errTargetRejected
		}
		return nil
	}
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) != "" {
		return errTargetRejected
	}
	if t.project != devProjectID || t.database != devDatabaseID || t.userID != devDemoUserID {
		return errTargetRejected
	}
	return nil
}

func validateLoopbackHost(value string) error {
	host, portValue, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portValue)
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return errTargetRejected
	}
	return nil
}

func openPasswordFD(fd int) (io.ReadCloser, error) {
	if fd < 0 || fd == 1 || fd == 2 {
		return nil, errPasswordInput
	}
	var file *os.File
	if fd == 0 {
		file = os.Stdin
	} else {
		file = os.NewFile(uintptr(fd), "password-input")
		if file == nil {
			return nil, errPasswordInput
		}
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		if fd != 0 && file != nil {
			_ = file.Close()
		}
		return nil, errPasswordInput
	}
	if fd == 0 {
		return io.NopCloser(file), nil
	}
	return file, nil
}

func readPassword(input io.Reader) ([]byte, error) {
	if input == nil {
		return nil, errPasswordInput
	}
	password, err := io.ReadAll(io.LimitReader(input, maxPasswordBytes+1))
	if err != nil || len(password) > maxPasswordBytes {
		wipe(password)
		return nil, errPasswordInput
	}
	return password, nil
}

func validPassword(password []byte) bool {
	return len(password) >= 8 && len(password) <= maxPasswordBytes
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func rotate(ctx context.Context, t target, store rotationStore, apply bool, password []byte) (rotationReceipt, error) {
	if store == nil {
		return rotationReceipt{}, errStoreUnavailable
	}
	before, err := store.readUser(ctx, t.userID)
	if err != nil {
		if errors.Is(err, errUserMissing) {
			return rotationReceipt{}, errUserMissing
		}
		return rotationReceipt{}, errStoreUnavailable
	}
	if !before.user.Active() {
		return rotationReceipt{}, errUserInactive
	}
	canonicalEmail := auth.CanonicalizeEmail(before.user.Email)
	if canonicalEmail == "" || (before.user.EmailCanonical != "" && before.user.EmailCanonical != canonicalEmail) {
		return rotationReceipt{}, errIdentityInvalid
	}
	if before.user.PasswordHash == "" {
		return rotationReceipt{}, errNoPasswordLogin
	}

	receipt, err := newReceipt(t, apply)
	if err != nil {
		return rotationReceipt{}, errors.New("unable to create result handle")
	}
	if !apply {
		receipt.Outcome = "ready"
		return receipt, nil
	}
	if !validPassword(password) {
		return rotationReceipt{}, errPasswordInvalid
	}
	compareErr := bcrypt.CompareHashAndPassword([]byte(before.user.PasswordHash), password)
	if compareErr == nil {
		receipt.Outcome = "rejected"
		return receipt, errPasswordNoop
	}
	if !errors.Is(compareErr, bcrypt.ErrMismatchedHashAndPassword) {
		return rotationReceipt{}, errNoPasswordLogin
	}

	newHash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return rotationReceipt{}, errPasswordInvalid
	}
	updateErr := store.updatePasswordHash(ctx, t.userID, before, string(newHash))
	if errors.Is(updateErr, errRotationConflict) {
		receipt.Outcome = "conflict"
		return receipt, errRotationConflict
	}
	// A transaction error may be ambiguous. Read back once, but never retry and
	// never report success when the transaction itself returned an error.
	after, readErr := store.readUser(ctx, t.userID)
	if readErr != nil {
		receipt.Outcome = "unknown"
		return receipt, errRotationUnknown
	}
	if updateErr != nil || !matchesReplacement(before, after, string(newHash), password) {
		receipt.Outcome = "unknown"
		return receipt, errRotationUnknown
	}
	receipt.Outcome = "replaced"
	return receipt, nil
}

func newReceipt(t target, apply bool) (rotationReceipt, error) {
	handle := make([]byte, 12)
	if _, err := rand.Read(handle); err != nil {
		return rotationReceipt{}, err
	}
	action := "dry_run"
	if apply {
		action = "password_replace"
	}
	return rotationReceipt{
		Project: t.project, Database: t.database, UserID: t.userID,
		Action: action, ResultHandle: hex.EncodeToString(handle),
	}, nil
}

func matchesReplacement(before, after rotationSnapshot, newHash string, password []byte) bool {
	return after.user.Active() &&
		after.user.PasswordHash == newHash &&
		reflect.DeepEqual(withoutPasswordHash(before.data), withoutPasswordHash(after.data)) &&
		bcrypt.CompareHashAndPassword([]byte(after.user.PasswordHash), password) == nil
}

func withoutPasswordHash(data map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{}, len(data))
	for key, value := range data {
		if key != "password_hash" {
			copy[key] = value
		}
	}
	return copy
}

type firestoreRotationStore struct {
	client *firestore.Client
}

func newFirestoreStore(ctx context.Context, t target) (rotationStore, func(), error) {
	if t.emulatorHost != "" {
		conn, err := grpc.DialContext(ctx, t.emulatorHost, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, errStoreUnavailable
		}
		client, err := firestore.NewClientWithDatabase(ctx, t.project, t.database,
			option.WithGRPCConn(conn), option.WithoutAuthentication())
		if err != nil {
			_ = conn.Close()
			return nil, nil, errStoreUnavailable
		}
		// Firestore Client.Close closes the supplied gRPC connection pool.
		return &firestoreRotationStore{client: client}, func() { _ = client.Close() }, nil
	}
	client, err := firestore.NewClientWithDatabase(ctx, t.project, t.database)
	if err != nil {
		return nil, nil, errStoreUnavailable
	}
	return &firestoreRotationStore{client: client}, func() { _ = client.Close() }, nil
}

func (s *firestoreRotationStore) readUser(ctx context.Context, userID string) (rotationSnapshot, error) {
	if s == nil || s.client == nil || !auth.ValidPathSegment(userID) {
		return rotationSnapshot{}, errStoreUnavailable
	}
	doc, err := s.client.Collection("users").Doc(userID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return rotationSnapshot{}, errUserMissing
	}
	if err != nil {
		return rotationSnapshot{}, errStoreUnavailable
	}
	var user auth.UserRecord
	if err := doc.DataTo(&user); err != nil {
		return rotationSnapshot{}, errStoreUnavailable
	}
	canonicalEmail := auth.CanonicalizeEmail(user.Email)
	if canonicalEmail == "" || (user.EmailCanonical != "" && user.EmailCanonical != canonicalEmail) {
		return rotationSnapshot{}, errIdentityInvalid
	}
	if user.PasswordHash == "" {
		return rotationSnapshot{}, errNoPasswordLogin
	}
	resolvedID, resolvedUser, err := auth.NewIdentityRepository(s.client).GetPasswordUserByEmail(ctx, canonicalEmail)
	if err != nil {
		return rotationSnapshot{}, errStoreUnavailable
	}
	if resolvedID != userID || resolvedUser == nil || resolvedUser.Email != user.Email || resolvedUser.PasswordHash != user.PasswordHash {
		return rotationSnapshot{}, errIdentityInvalid
	}
	return rotationSnapshot{user: user, data: doc.Data()}, nil
}

func (s *firestoreRotationStore) updatePasswordHash(ctx context.Context, userID string, expected rotationSnapshot, passwordHash string) error {
	if s == nil || s.client == nil || !auth.ValidPathSegment(userID) || passwordHash == "" {
		return errStoreUnavailable
	}
	ref := s.client.Collection("users").Doc(userID)
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return errRotationConflict
		}
		if err != nil {
			return errStoreUnavailable
		}
		var current auth.UserRecord
		if err := doc.DataTo(&current); err != nil || !current.Active() || current.PasswordHash != expected.user.PasswordHash {
			return errRotationConflict
		}
		if !reflect.DeepEqual(withoutPasswordHash(expected.data), withoutPasswordHash(doc.Data())) {
			return errRotationConflict
		}
		return tx.Update(ref, []firestore.Update{{Path: "password_hash", Value: passwordHash}})
	})
	if errors.Is(err, errRotationConflict) {
		return errRotationConflict
	}
	if err != nil {
		return errStoreUnavailable
	}
	return nil
}
