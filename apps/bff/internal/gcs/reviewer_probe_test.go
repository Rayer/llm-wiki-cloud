package gcs
import (
 "context"
 "io"
 "net/http"
 "strings"
 "testing"
 cloudstorage "cloud.google.com/go/storage"
 "github.com/rayer/llm-wiki-bff/internal/llm"
 "google.golang.org/api/option"
)
type reviewerTransport struct{calls int}
func (r *reviewerTransport) RoundTrip(req *http.Request)(*http.Response,error){r.calls++;return &http.Response{StatusCode:403,Header:make(http.Header),Body:io.NopCloser(strings.NewReader("<Error><Code>AccessDenied</Code></Error>")),Request:req},nil}
func TestReviewerArtifactSDKHost(t *testing.T){
 tr:=&reviewerTransport{}
 sdk,err:=cloudstorage.NewClient(context.Background(),option.WithoutAuthentication(),option.WithHTTPClient(&http.Client{Transport:tr}));if err!=nil{t.Fatal(err)};defer sdk.Close()
 c:=&Client{bucket:sdk.Bucket("reviewer-fixture"),userID:"u",projectID:"p"}
 receipt:=&gcsHostReceipt{}
 ctx:=llm.WithCallStage(llm.WithHostCallRecorder(context.Background(),receipt),"profile.snapshot")
 _,err=c.ReadFileLimited(ctx,".lwc/profile/tags/sets/v1/fixture.json",1024)
 t.Logf("SDK requests=%d host receipt finished=%d read failed=%v",tr.calls,receipt.finished,err!=nil)
 if tr.calls!=1 || err==nil {t.Fatal("fixture did not exercise SDK")}
 if receipt.finished!=1 {t.Fatal("real SDK request has no host receipt")}
}
