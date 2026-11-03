package events

import (
	"context"
	"testing"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/media"
)

type libs map[string]bool

func (l libs) CanLibrary(_ context.Context, actor authorization.Actor, id string, _ media.Permission) error {
	if authorization.IsAdmin(actor) || l[actor.UserID+"/"+id] {
		return nil
	}
	return core.ErrNotFound()
}

func TestCanReceive(t *testing.T) {
	ctx := context.Background()
	perms := libs{"u1/lib": true}
	u1 := authorization.Actor{UserID: "u1", Role: media.RoleUser}
	u2 := authorization.Actor{UserID: "u2", Role: media.RoleUser}
	admin := authorization.Actor{UserID: "a", Role: media.RoleAdmin}
	cases := []struct {
		e    Event
		want map[string]bool
	}{
		{Event{UserID: "u1"}, map[string]bool{"u1": true, "u2": false, "a": false}},
		{Event{LibraryID: "lib"}, map[string]bool{"u1": true, "u2": false, "a": true}},
		{Event{AdminOnly: true}, map[string]bool{"u1": false, "u2": false, "a": true}},
		{Event{}, map[string]bool{"u1": false, "u2": false, "a": false}}, // Sin destinatario, nadie.
	}
	for i, c := range cases {
		for _, a := range []authorization.Actor{u1, u2, admin} {
			if got := CanReceive(ctx, perms, a, c.e); got != c.want[a.UserID] {
				t.Errorf("caso %d, %s: %v", i, a.UserID, got)
			}
		}
	}
}

// Un suscriptor que no lee se descarta en vez de frenar a quien publica.
func TestSuscriptorLentoSeDescarta(t *testing.T) {
	b := NewBus()
	slow, fast := b.Subscribe(), b.Subscribe()
	for i := range subscriberBuffer + 1 {
		b.Publish(Event{Name: "x", UserID: "u"})
		if i < subscriberBuffer {
			<-fast.C
		}
	}
	select {
	case <-slow.Dropped:
	default:
		t.Fatal("el lento debía descartarse")
	}
	if b.Subscribers() != 1 {
		t.Fatalf("%d suscriptores", b.Subscribers())
	}
	b.Unsubscribe(slow) // Retirar uno ya descartado no falla.
	e := <-fast.C
	if e.Time.IsZero() {
		t.Fatal("Publish pone la hora")
	}
	var nilBus *Bus
	nilBus.Publish(Event{}) // Sin bus no pasa nada.
}
