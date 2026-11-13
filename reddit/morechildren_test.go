package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testCommentData returns the JSON of a comment's "data" object.
func testCommentData(id, parentID, postID, body string) string {
	return fmt.Sprintf(`{
		"id": %q,
		"name": %q,
		"parent_id": %q,
		"link_id": %q,
		"body": %q,
		"author": "testuser",
		"replies": ""
	}`, id, kindComment+"_"+id, parentID, postID, body)
}

// testCommentThing returns the JSON of a comment thing (kind + data).
func testCommentThing(id, parentID, postID, body string) string {
	return fmt.Sprintf(`{"kind": %q, "data": %s}`, kindComment, testCommentData(id, parentID, postID, body))
}

// testMoreThing returns the JSON of a more thing (kind + data).
func testMoreThing(id, parentID string, children ...string) string {
	childrenJSON, _ := json.Marshal(children)
	return fmt.Sprintf(`{
		"kind": %q,
		"data": {
			"id": %q,
			"name": %q,
			"parent_id": %q,
			"count": %d,
			"depth": 1,
			"children": %s
		}
	}`, kindMore, id, kindComment+"_"+id, parentID, len(children), childrenJSON)
}

// testMoreChildrenResponse wraps things into an api/morechildren response body.
func testMoreChildrenResponse(things ...string) string {
	return `{"json": {"errors": [], "data": {"things": [` + strings.Join(things, ",") + `]}}}`
}

// testComment builds a *Comment equivalent to what testCommentThing would parse into.
func testComment(t testing.TB, id, parentID, postID, body string) *Comment {
	comment := new(Comment)
	require.NoError(t, json.Unmarshal([]byte(testCommentData(id, parentID, postID, body)), comment))
	return comment
}

// handleMoreChildren registers an /api/morechildren handler returning the given body.
func handleMoreChildren(t testing.TB, mux *http.ServeMux, body string) {
	mux.HandleFunc("/api/morechildren", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		err := r.ParseForm()
		require.NoError(t, err)
		require.Equal(t, "json", r.PostForm.Get("api_type"))

		fmt.Fprint(w, body)
	})
}

// commentIDs returns the full IDs of the comments, in order.
func commentIDs(comments []*Comment) []string {
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		ids = append(ids, c.FullID)
	}
	return ids
}

func TestPostService_LoadMoreComments_OutOfOrderMultiLevel(t *testing.T) {
	client, mux := setup(t)

	// children arrive before their parents, across multiple levels.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("mno", "t1_jkl", "t3_123", "level 4"),
		testCommentThing("jkl", "t1_ghi", "t3_123", "level 3"),
		testCommentThing("ghi", "t1_def", "t3_123", "level 2"),
		testCommentThing("def", "t1_abc", "t3_123", "level 1"),
	))

	pc := &PostAndComments{
		Post: &Post{FullID: "t3_123"},
		Comments: []*Comment{
			testComment(t, "abc", "t3_123", "t3_123", "top"),
		},
		More: &More{Children: []string{"mno", "jkl", "ghi", "def"}},
	}

	_, err := client.Post.LoadMoreComments(ctx, pc)
	require.NoError(t, err)

	// no node may be lost despite the reversed arrival order.
	require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
	def := pc.Comments[0].Replies.Comments
	require.Equal(t, []string{"t1_def"}, commentIDs(def))
	ghi := def[0].Replies.Comments
	require.Equal(t, []string{"t1_ghi"}, commentIDs(ghi))
	jkl := ghi[0].Replies.Comments
	require.Equal(t, []string{"t1_jkl"}, commentIDs(jkl))
	mno := jkl[0].Replies.Comments
	require.Equal(t, []string{"t1_mno"}, commentIDs(mno))
	require.Equal(t, "level 4", mno[0].Body)

	// the more cursor is consumed.
	require.Nil(t, pc.More)
	require.False(t, pc.HasMore())
}

func TestPostService_LoadMoreComments_SiblingOrder(t *testing.T) {
	client, mux := setup(t)

	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("new2", "t3_123", "t3_123", "new top 2"),
		testCommentThing("reply2", "t1_a", "t3_123", "reply 2"),
		testCommentThing("new1", "t3_123", "t3_123", "new top 1"),
		testCommentThing("reply1", "t1_a", "t3_123", "reply 1"),
	))

	existingReply := testComment(t, "reply0", "t1_a", "t3_123", "existing reply")
	a := testComment(t, "a", "t3_123", "t3_123", "a")
	a.Replies.Comments = []*Comment{existingReply}

	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{a, testComment(t, "b", "t3_123", "t3_123", "b")},
		More:     &More{Children: []string{"new1", "new2", "reply1", "reply2"}},
	}

	_, err := client.Post.LoadMoreComments(ctx, pc)
	require.NoError(t, err)

	// existing siblings keep their order; new siblings are appended
	// in the order they first appear in the response.
	require.Equal(t, []string{"t1_a", "t1_b", "t1_new2", "t1_new1"}, commentIDs(pc.Comments))
	require.Equal(t, []string{"t1_reply0", "t1_reply2", "t1_reply1"}, commentIDs(a.Replies.Comments))
	require.Nil(t, pc.More)
}

func TestPostService_LoadMoreComments_Duplicates(t *testing.T) {
	client, mux := setup(t)

	// def appears twice in the response; abc is already in the tree.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("def", "t1_abc", "t3_123", "first"),
		testCommentThing("abc", "t3_123", "t3_123", "top"),
		testCommentThing("def", "t1_abc", "t3_123", "first"),
		testCommentThing("ghi", "t1_def", "t3_123", "child of def"),
	))

	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
		More:     &More{Children: []string{"def", "ghi"}},
	}

	_, err := client.Post.LoadMoreComments(ctx, pc)
	require.NoError(t, err)

	// the response is idempotent: no duplicate nodes are appended.
	require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
	require.Equal(t, []string{"t1_def"}, commentIDs(pc.Comments[0].Replies.Comments))
	// children of the duplicated comment attach to the existing node.
	require.Equal(t, []string{"t1_ghi"}, commentIDs(pc.Comments[0].Replies.Comments[0].Replies.Comments))
	require.Nil(t, pc.More)
}

func TestPostService_LoadMoreComments_ConflictingContent(t *testing.T) {
	client, mux := setup(t)

	// same ID as the existing comment, different body.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("abc", "t3_123", "t3_123", "edited"),
	))

	more := &More{Children: []string{"abc"}}
	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
		More:     more,
	}

	_, err := client.Post.LoadMoreComments(ctx, pc)
	require.EqualError(t, err, "comment t1_abc: conflicting content")

	// the tree and the more cursor are unchanged.
	require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
	require.Equal(t, "top", pc.Comments[0].Body)
	require.Empty(t, pc.Comments[0].Replies.Comments)
	require.Same(t, more, pc.More)
	require.Equal(t, []string{"abc"}, pc.More.Children)
}

func TestPostService_LoadMoreComments_InvalidRelations(t *testing.T) {
	tests := []struct {
		name     string
		response string
		err      string
	}{
		{
			name: "orphan comment",
			response: testMoreChildrenResponse(
				testCommentThing("def", "t1_unknown", "t3_123", "orphan"),
			),
			err: "comment t1_def: parent t1_unknown not found in the comment tree",
		},
		{
			name: "orphan more",
			response: testMoreChildrenResponse(
				testMoreThing("m1", "t1_unknown", "x", "y"),
			),
			err: "more t1_m1: parent t1_unknown not found in the comment tree",
		},
		{
			name: "cycle",
			response: testMoreChildrenResponse(
				testCommentThing("c1", "t1_c2", "t3_123", "c1"),
				testCommentThing("c2", "t1_c1", "t3_123", "c2"),
			),
			err: "comment t1_c1: cycle detected in the comment tree",
		},
		{
			name: "self parent",
			response: testMoreChildrenResponse(
				testCommentThing("c1", "t1_c1", "t3_123", "c1"),
			),
			err: "comment t1_c1: cycle detected in the comment tree",
		},
		{
			name: "cross post",
			response: testMoreChildrenResponse(
				testCommentThing("def", "t1_abc", "t3_other", "wrong post"),
			),
			err: "comment t1_def belongs to post t3_other, not t3_123",
		},
		{
			name: "conflict within response",
			response: testMoreChildrenResponse(
				testCommentThing("def", "t1_abc", "t3_123", "one"),
				testCommentThing("def", "t1_abc", "t3_123", "two"),
			),
			err: "comment t1_def: conflicting content",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, mux := setup(t)
			handleMoreChildren(t, mux, test.response)

			more := &More{Children: []string{"def"}}
			pc := &PostAndComments{
				Post:     &Post{FullID: "t3_123"},
				Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
				More:     more,
			}

			_, err := client.Post.LoadMoreComments(ctx, pc)
			require.EqualError(t, err, test.err)

			// the tree and the more cursor are unchanged.
			require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
			require.Empty(t, pc.Comments[0].Replies.Comments)
			require.Same(t, more, pc.More)
			require.Equal(t, []string{"def"}, pc.More.Children)
		})
	}
}

func TestPostService_LoadMoreComments_MergeMores(t *testing.T) {
	client, mux := setup(t)

	// two mores for the same parent, plus one for the post itself.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("def", "t1_abc", "t3_123", "child"),
		testMoreThing("m1", "t1_abc", "x", "y"),
		testMoreThing("m2", "t3_123", "q"),
		testMoreThing("m3", "t1_abc", "y", "z"),
	))

	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
		More:     &More{Children: []string{"def"}},
	}

	_, err := client.Post.LoadMoreComments(ctx, pc)
	require.NoError(t, err)

	require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
	require.Equal(t, []string{"t1_def"}, commentIDs(pc.Comments[0].Replies.Comments))

	// the mores for t1_abc are merged, children deduplicated in order.
	abcMore := pc.Comments[0].Replies.More
	require.NotNil(t, abcMore)
	require.Equal(t, []string{"x", "y", "z"}, abcMore.Children)

	// the post-level more replaces the consumed cursor.
	require.NotNil(t, pc.More)
	require.Equal(t, []string{"q"}, pc.More.Children)
}

func TestPostService_LoadMoreComments_NoMoreForRoot(t *testing.T) {
	client, mux := setup(t)

	// the response only contains a more for a comment, none for the post.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("def", "t1_abc", "t3_123", "child"),
		testMoreThing("m1", "t1_def", "x"),
	))

	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
		More:     &More{Children: []string{"def"}},
	}

	_, err := client.Post.LoadMoreComments(ctx, pc)
	require.NoError(t, err)

	require.Nil(t, pc.More)
	require.False(t, pc.HasMore())

	def := pc.Comments[0].Replies.Comments[0]
	require.Equal(t, []string{"x"}, def.Replies.More.Children)
	require.True(t, def.HasMore())
}

func TestPostService_LoadMoreComments_HTTPError(t *testing.T) {
	client, mux := setup(t)

	mux.HandleFunc("/api/morechildren", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	more := &More{Children: []string{"def"}}
	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
		More:     more,
	}

	resp, err := client.Post.LoadMoreComments(ctx, pc)
	require.Error(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	// the tree and the more cursor are unchanged.
	require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
	require.Same(t, more, pc.More)
	require.Equal(t, []string{"def"}, pc.More.Children)
}

func TestPostService_LoadMoreComments_ContextCanceled(t *testing.T) {
	client, mux := setup(t)

	mux.HandleFunc("/api/morechildren", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testMoreChildrenResponse(testCommentThing("def", "t1_abc", "t3_123", "child")))
	})

	more := &More{Children: []string{"def"}}
	pc := &PostAndComments{
		Post:     &Post{FullID: "t3_123"},
		Comments: []*Comment{testComment(t, "abc", "t3_123", "t3_123", "top")},
		More:     more,
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Post.LoadMoreComments(canceledCtx, pc)
	require.True(t, errors.Is(err, context.Canceled))

	// the tree and the more cursor are unchanged.
	require.Equal(t, []string{"t1_abc"}, commentIDs(pc.Comments))
	require.Same(t, more, pc.More)
	require.Equal(t, []string{"def"}, pc.More.Children)
}

func TestCommentService_LoadMoreReplies_OutOfOrderMergeMores(t *testing.T) {
	client, mux := setup(t)

	// the grandchild arrives before its parent; two mores share a parent.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("ghi", "t1_def", "t3_123", "grandchild"),
		testMoreThing("m1", "t1_def", "x", "y"),
		testCommentThing("def", "t1_abc", "t3_123", "child"),
		testMoreThing("m2", "t1_def", "y", "z"),
	))

	more := &More{Children: []string{"def", "ghi"}}
	comment := &Comment{
		FullID: "t1_abc",
		PostID: "t3_123",
		Replies: Replies{
			More: more,
		},
	}

	_, err := client.Comment.LoadMoreReplies(ctx, comment)
	require.NoError(t, err)

	require.Equal(t, []string{"t1_def"}, commentIDs(comment.Replies.Comments))
	def := comment.Replies.Comments[0]
	require.Equal(t, []string{"t1_ghi"}, commentIDs(def.Replies.Comments))

	// the mores for t1_def are merged, children deduplicated in order.
	require.NotNil(t, def.Replies.More)
	require.Equal(t, []string{"x", "y", "z"}, def.Replies.More.Children)

	// the comment's own more cursor is consumed.
	require.Nil(t, comment.Replies.More)
	require.False(t, comment.HasMore())
}

func TestCommentService_LoadMoreReplies_DuplicateResponse(t *testing.T) {
	client, mux := setup(t)

	// the exact same comment arrives twice.
	handleMoreChildren(t, mux, testMoreChildrenResponse(
		testCommentThing("def", "t1_abc", "t3_123", "child"),
		testCommentThing("def", "t1_abc", "t3_123", "child"),
	))

	comment := &Comment{
		FullID: "t1_abc",
		PostID: "t3_123",
		Replies: Replies{
			More: &More{Children: []string{"def"}},
		},
	}

	_, err := client.Comment.LoadMoreReplies(ctx, comment)
	require.NoError(t, err)

	require.Equal(t, []string{"t1_def"}, commentIDs(comment.Replies.Comments))
	require.Nil(t, comment.Replies.More)
}

func TestCommentService_LoadMoreReplies_InvalidRelations(t *testing.T) {
	tests := []struct {
		name     string
		response string
		err      string
	}{
		{
			name: "orphan",
			response: testMoreChildrenResponse(
				testCommentThing("def", "t1_unknown", "t3_123", "orphan"),
			),
			err: "comment t1_def: parent t1_unknown not found in the comment tree",
		},
		{
			name: "cycle",
			response: testMoreChildrenResponse(
				testCommentThing("c1", "t1_c2", "t3_123", "c1"),
				testCommentThing("c2", "t1_c1", "t3_123", "c2"),
			),
			err: "comment t1_c1: cycle detected in the comment tree",
		},
		{
			name: "cross post",
			response: testMoreChildrenResponse(
				testCommentThing("def", "t1_abc", "t3_other", "wrong post"),
			),
			err: "comment t1_def belongs to post t3_other, not t3_123",
		},
		{
			name: "conflicting content",
			response: testMoreChildrenResponse(
				testCommentThing("def", "t1_abc", "t3_123", "one"),
				testCommentThing("def", "t1_abc", "t3_123", "two"),
			),
			err: "comment t1_def: conflicting content",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, mux := setup(t)
			handleMoreChildren(t, mux, test.response)

			more := &More{Children: []string{"def"}}
			comment := &Comment{
				FullID: "t1_abc",
				PostID: "t3_123",
				Replies: Replies{
					More: more,
				},
			}

			_, err := client.Comment.LoadMoreReplies(ctx, comment)
			require.EqualError(t, err, test.err)

			// the tree and the more cursor are unchanged.
			require.Empty(t, comment.Replies.Comments)
			require.Same(t, more, comment.Replies.More)
			require.Equal(t, []string{"def"}, comment.Replies.More.Children)
		})
	}
}

func TestCommentService_LoadMoreReplies_HTTPError(t *testing.T) {
	client, mux := setup(t)

	mux.HandleFunc("/api/morechildren", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	more := &More{Children: []string{"def"}}
	comment := &Comment{
		FullID: "t1_abc",
		PostID: "t3_123",
		Replies: Replies{
			More: more,
		},
	}

	resp, err := client.Comment.LoadMoreReplies(ctx, comment)
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	require.Empty(t, comment.Replies.Comments)
	require.Same(t, more, comment.Replies.More)
}

func TestCommentService_LoadMoreReplies_ContextCanceled(t *testing.T) {
	client, mux := setup(t)

	mux.HandleFunc("/api/morechildren", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testMoreChildrenResponse(testCommentThing("def", "t1_abc", "t3_123", "child")))
	})

	more := &More{Children: []string{"def"}}
	comment := &Comment{
		FullID: "t1_abc",
		PostID: "t3_123",
		Replies: Replies{
			More: more,
		},
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Comment.LoadMoreReplies(canceledCtx, comment)
	require.True(t, errors.Is(err, context.Canceled))

	require.Empty(t, comment.Replies.Comments)
	require.Same(t, more, comment.Replies.More)
}
