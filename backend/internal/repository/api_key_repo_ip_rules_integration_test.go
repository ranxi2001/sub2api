//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestAPIKeyUpdatePreservesConcurrentOppositeACL(t *testing.T) {
	for _, column := range []string{"whitelist", "blacklist"} {
		for _, clear := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/clear=%t", column, clear), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				// A private Ent client keeps the scheduling hook out of other tests.
				client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
				repo := newAPIKeyRepositoryWithSQL(client, integrationDB)
				user := mustCreateUser(t, client, &service.User{})
				key := &service.APIKey{
					UserID: user.ID, Key: fmt.Sprintf("sk-acl-interleave-%d", user.ID),
					Name: "acl interleave", Status: service.StatusActive,
					IPWhitelist: []string{"192.0.2.0/24"}, IPBlacklist: []string{"192.0.2.8"},
				}
				if !clear {
					key.IPWhitelist, key.IPBlacklist = nil, nil
				}
				require.NoError(t, repo.Create(ctx, key))
				t.Cleanup(func() {
					_, err := integrationDB.Exec("DELETE FROM api_keys WHERE id=$1", key.ID)
					require.NoError(t, err)
					_, err = integrationDB.Exec("DELETE FROM users WHERE id=$1", user.ID)
					require.NoError(t, err)
					// Deletion itself can enqueue cache invalidations via DB triggers.
					_, err = integrationDB.Exec(`DELETE FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to($1,'UTF8')),'hex')`, key.Key)
					require.NoError(t, err)
				})
				type pausedWriter struct{}
				paused, resume := make(chan struct{}), make(chan struct{})
				var once sync.Once
				release := func() { once.Do(func() { close(resume) }) }
				client.APIKey.Use(func(next dbent.Mutator) dbent.Mutator {
					return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
						if ctx.Value(pausedWriter{}) == true {
							// APIKeyService has read its snapshot and selected its fields;
							// the real adapter's SQL has not executed yet.
							close(paused)
							select {
							case <-resume:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
						return next.Mutate(ctx, m)
					})
				})
				svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)
				allow, deny := []string{"203.0.113.0/24"}, []string{"203.0.113.8"}
				empty := []string{}
				var first, second service.UpdateAPIKeyRequest
				if column == "whitelist" {
					first.IPWhitelist, second.IPBlacklist = &allow, &deny
					if clear {
						first.IPWhitelist = &empty
					}
				} else {
					first.IPBlacklist, second.IPWhitelist = &deny, &allow
					if clear {
						first.IPBlacklist = &empty
					}
				}
				done := make(chan error, 1)
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					_, err := svc.Update(context.WithValue(ctx, pausedWriter{}, true), key.ID, user.ID, first)
					done <- err
				}()
				defer func() {
					cancel()
					release()
					select {
					case <-finished:
					case <-time.After(3 * time.Second):
						t.Error("cancelled ACL writer did not exit before fixture cleanup")
					}
				}()
				select {
				case <-paused:
				case <-ctx.Done():
					t.Fatal("first ACL edit did not reach its SQL boundary")
				}
				_, err := svc.Update(ctx, key.ID, user.ID, second)
				require.NoError(t, err, "opposite ACL must commit while the first edit is paused")
				release()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal("first ACL edit did not finish")
				}
				got, err := repo.GetByID(ctx, key.ID)
				require.NoError(t, err)
				if clear && column == "whitelist" {
					require.Empty(t, got.IPWhitelist)
				} else {
					require.Equal(t, allow, got.IPWhitelist, "concurrent whitelist must survive stale opposite ACL edit")
				}
				if clear && column == "blacklist" {
					require.Empty(t, got.IPBlacklist)
				} else {
					require.Equal(t, deny, got.IPBlacklist, "concurrent blacklist must survive stale opposite ACL edit")
					allowed, _ := ip.CheckIPRestrictionWithCompiledRules("203.0.113.8", ip.CompileIPRules(got.IPWhitelist), ip.CompileIPRules(got.IPBlacklist))
					require.False(t, allowed, "concurrent deny must retain precedence")
				}
			})
		}
	}
}
