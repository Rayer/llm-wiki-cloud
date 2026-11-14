package nudge

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	pb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This exercises the pinned Firestore SDK's actual struct serializer and
// snapshot decoder over an in-memory gRPC transport; it is not an emulator.
type serializerProbeServer struct {
	pb.UnimplementedFirestoreServer
	documents map[string]*pb.Document
	last      *pb.Document
}

func (s *serializerProbeServer) Commit(_ context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	now := timestamppb.Now()
	results := make([]*pb.WriteResult, len(req.Writes))
	if s.documents == nil {
		s.documents = make(map[string]*pb.Document)
	}
	for i, write := range req.Writes {
		doc := write.GetUpdate()
		doc.CreateTime, doc.UpdateTime = now, now
		s.documents[doc.Name] = doc
		s.last = doc
		results[i] = &pb.WriteResult{UpdateTime: now}
	}
	return &pb.CommitResponse{CommitTime: now, WriteResults: results}, nil
}

func (s *serializerProbeServer) BatchGetDocuments(req *pb.BatchGetDocumentsRequest, stream pb.Firestore_BatchGetDocumentsServer) error {
	for _, name := range req.Documents {
		var response *pb.BatchGetDocumentsResponse
		if doc := s.documents[name]; doc != nil {
			response = &pb.BatchGetDocumentsResponse{Result: &pb.BatchGetDocumentsResponse_Found{Found: doc}, ReadTime: timestamppb.Now()}
		} else {
			response = &pb.BatchGetDocumentsResponse{Result: &pb.BatchGetDocumentsResponse_Missing{Missing: name}, ReadTime: timestamppb.Now()}
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func TestSDKObservationRoundTripPreservesWireAndHTTPFields(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	wire := &serializerProbeServer{}
	pb.RegisterFirestoreServer(server, wire)
	go server.Serve(listener)
	defer server.Stop()
	defer listener.Close()
	conn, err := grpc.NewClient("passthrough:///serializer-probe", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client, err := firestore.NewClient(ctx, "nudge-serializer-probe", option.WithGRPCConn(conn), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	original := Slot{
		SchemaVersion: SchemaVersion, SourceKey: "content-purpose-gap:project-a", SourceRevision: 1, Active: true,
		Observation: &Observation{
			ProjectID: "project-a", ProjectName: "Named Project", Title: "Title", Body: "Basis",
			DeclaredPurpose: "Manual purpose", PublishedSummary: "Published overview", Severity: SeverityAttention,
			Coverage: Coverage{}, Actions: []Action{{ID: "copy-open", Kind: "copy-then-link", Label: "Open", CopyText: "Published overview",
				Target: &Target{Kind: "project-description", ProjectID: "project-a"}}},
		},
	}
	ref := client.Collection("review").Doc("slot")
	if _, err := ref.Set(ctx, original); err != nil {
		t.Fatal(err)
	}
	doc := wire.last
	if doc == nil {
		t.Fatal("FireStore SDK Commit was not intercepted")
	}
	observationFields := doc.Fields["observation"].GetMapValue().Fields
	for _, key := range []string{"project_id", "project_name", "declared_purpose", "published_summary", "coverage", "actions"} {
		if _, ok := observationFields[key]; !ok {
			t.Errorf("serialized observation lacks JSON wire key %q: %v", key, observationFields)
		}
	}
	coverageFields := observationFields["coverage"].GetMapValue().Fields
	for _, key := range []string{"total_concepts", "selected_concepts", "sampled", "title_truncated_concepts", "body_truncated_concepts"} {
		if _, ok := coverageFields[key]; !ok {
			t.Errorf("serialized zero-valued coverage lacks key %q: %v", key, coverageFields)
		}
	}
	actionFields := observationFields["actions"].GetArrayValue().Values[0].GetMapValue().Fields
	if actionFields["copy_text"].GetStringValue() != "Published overview" ||
		actionFields["target"].GetMapValue().Fields["project_id"].GetStringValue() != "project-a" {
		t.Fatalf("serialized action lost copy text or same-Project target: %v", actionFields)
	}

	snapshot, err := ref.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSlot(snapshot.Data())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Observation == nil || decoded.Observation.ProjectID != "project-a" || decoded.Observation.ProjectName != "Named Project" ||
		decoded.Observation.DeclaredPurpose != "Manual purpose" || decoded.Observation.PublishedSummary != "Published overview" ||
		decoded.Observation.Coverage != (Coverage{}) || len(decoded.Observation.Actions) != 1 ||
		decoded.Observation.Actions[0].CopyText != "Published overview" || decoded.Observation.Actions[0].Target == nil ||
		decoded.Observation.Actions[0].Target.ProjectID != "project-a" {
		t.Fatalf("SDK snapshot -> production decodeSlot lost nested observation fields: %+v", decoded.Observation)
	}
	target, err := ResolveTarget(*decoded.Observation.Actions[0].Target)
	if err != nil || target != "/profile?project_id=project-a#project-description" {
		t.Fatalf("decoded target resolution=%q err=%v", target, err)
	}
	httpJSON, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Observation *Observation `json:"observation"`
	}
	if err := json.Unmarshal(httpJSON, &response); err != nil {
		t.Fatal(err)
	}
	if response.Observation == nil || response.Observation.Coverage != (Coverage{}) ||
		response.Observation.Actions[0].Target.ProjectID != "project-a" {
		t.Fatalf("HTTP JSON round trip lost target or zero coverage: %s", httpJSON)
	}
}
