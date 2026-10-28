package firestore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// newLocalFirestoreAuditOption is an opt-in local qualification hook. It logs
// Firestore RPC method and collection names only; document IDs and values are
// never written. The numeric loopback emulator requirement prevents this hook
// from being used against cloud Firestore.
func newLocalFirestoreAuditConnection() (*grpc.ClientConn, bool, error) {
	path := strings.TrimSpace(os.Getenv("GAT4_FIRESTORE_AUDIT_FILE"))
	if path == "" {
		return nil, false, nil
	}
	if !filepath.IsAbs(path) {
		return nil, false, errors.New("GAT4 Firestore audit path must be absolute")
	}
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
		return nil, false, errors.New("GAT4 Firestore audit requires a numeric loopback emulator")
	}
	interceptors, err := newFirestoreAuditInterceptors(path)
	if err != nil {
		return nil, false, err
	}
	conn, err := grpc.Dial(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(interceptors.unary),
		grpc.WithStreamInterceptor(interceptors.stream),
	)
	if err != nil {
		return nil, false, fmt.Errorf("dial GAT4 Firestore emulator: %w", err)
	}
	return conn, true, nil
}

type firestoreAuditInterceptors struct {
	unary  grpc.UnaryClientInterceptor
	stream grpc.StreamClientInterceptor
	path   string
	mu     sync.Mutex
}

func newFirestoreAuditInterceptors(path string) (*firestoreAuditInterceptors, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open GAT4 Firestore audit file: %w", err)
	}
	if _, err := file.WriteString("# GAT4 Firestore RPC audit v1; collection names only\n"); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("initialize GAT4 Firestore audit file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("initialize GAT4 Firestore audit file: %w", err)
	}
	audit := &firestoreAuditInterceptors{path: path}
	audit.unary = func(ctx context.Context, method string, request, response any, connection *grpc.ClientConn,
		invoker grpc.UnaryInvoker, callOptions ...grpc.CallOption) error {
		if err := audit.record(method, request); err != nil {
			return err
		}
		return invoker(ctx, method, request, response, connection, callOptions...)
	}
	audit.stream = func(ctx context.Context, description *grpc.StreamDesc, connection *grpc.ClientConn, method string,
		streamer grpc.Streamer, callOptions ...grpc.CallOption) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, description, connection, method, callOptions...)
		if err != nil {
			return nil, err
		}
		return firestoreAuditClientStream{ClientStream: stream, method: method, audit: audit}, nil
	}
	return audit, nil
}

func (a *firestoreAuditInterceptors) record(method string, request any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return appendFirestoreAudit(a.path, formatFirestoreAudit(method, request))
}

type firestoreAuditClientStream struct {
	grpc.ClientStream
	method string
	audit  *firestoreAuditInterceptors
}

func (s firestoreAuditClientStream) SendMsg(message any) error {
	if err := s.audit.record(s.method, message); err != nil {
		return err
	}
	return s.ClientStream.SendMsg(message)
}

func appendFirestoreAudit(path, line string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("append GAT4 Firestore audit file: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("append GAT4 Firestore audit file: %w", err)
	}
	return nil
}

func formatFirestoreAudit(method string, request any) string {
	collections, forbidden := firestoreRequestCollections(request)
	return fmt.Sprintf("rpc=%s collections=%s forbidden=%s", method, strings.Join(collections, ","), strings.Join(forbidden, ","))
}

func firestoreRequestCollections(request any) ([]string, []string) {
	message, ok := request.(proto.Message)
	if !ok || message == nil {
		return nil, nil
	}
	collections := map[string]struct{}{}
	forbidden := map[string]struct{}{}
	var visit func(protoreflect.Message)
	visit = func(message protoreflect.Message) {
		message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			if field.IsMap() {
				return true
			}
			if field.IsList() {
				list := value.List()
				for index := 0; index < list.Len(); index++ {
					item := list.Get(index)
					if field.Kind() == protoreflect.MessageKind {
						visit(item.Message())
					} else if field.Kind() == protoreflect.StringKind {
						collectFirestoreString(field.Name(), item.String(), collections, forbidden)
					}
				}
				return true
			}
			switch field.Kind() {
			case protoreflect.MessageKind:
				visit(value.Message())
			case protoreflect.StringKind:
				collectFirestoreString(field.Name(), value.String(), collections, forbidden)
			}
			return true
		})
	}
	visit(message.ProtoReflect())
	return sortedAuditNames(collections), sortedAuditNames(forbidden)
}

func collectFirestoreString(field protoreflect.Name, value string, collections, forbidden map[string]struct{}) {
	if strings.Contains(value, "/documents/") {
		resource := value[strings.Index(value, "/documents/")+len("/documents/"):]
		segments := strings.Split(resource, "/")
		for index := 0; index < len(segments); index += 2 {
			if segments[index] != "" {
				classifyAuditCollection(segments[index], collections, forbidden)
			}
		}
	}
	if field == "collection_id" || field == "collectionId" {
		classifyAuditCollection(value, collections, forbidden)
	}
}

func classifyAuditCollection(name string, collections, forbidden map[string]struct{}) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if name == "projects" || strings.Contains(strings.ToLower(name), "binding") {
		forbidden[name] = struct{}{}
		return
	}
	if !auditCollectionName.MatchString(name) {
		name = "other"
	}
	collections[name] = struct{}{}
}

func sortedAuditNames(names map[string]struct{}) []string {
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

var auditCollectionName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
