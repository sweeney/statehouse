package config

import (
	"fmt"
	"sort"

	"github.com/sweeney/common/devices"
)

// CodePartOfRoomNotInherited is statehouse's own problem code, reported next to
// devices.Check's. It is prefixed "statehouse." rather than "device." because the
// shared vocabulary does not consider it a problem: see
// PartOfRoomNotInheritedProblems.
const CodePartOfRoomNotInherited = "statehouse.part_of_room_not_inherited"

// PartOfRoomNotInheritedProblems reports each device that declares no room of its
// own but is part_of a device that declares a place.
//
// In the shared vocabulary that record is fine: devices.PlaceOf hands the device its
// parent's place, so a probe "needn't repeat the fridge's room", and devices.Check
// says nothing. Statehouse deliberately does not call PlaceOf (#83). Room is relayed,
// never derived, and a probe declares its own room or has none. So exactly these
// records are the ones where statehouse and a PlaceOf-reading consumer answer
// differently, and an operator who wrote the namespace for PlaceOf would otherwise
// find the probe roomless with nothing saying why. This says why, and what to add:
//
//   - parent has a room: add that room, and the parent's floor when it has one,
//     since a probe declares both;
//   - parent has only the deprecated free-text location: that is not a
//     "<floor>.<room>" id, so suggesting it would trade this warning for
//     device.room_malformed. The parent has to be given a room first.
//
// It reports nothing when the answers agree anyway:
//   - the device declares a place (room, or the legacy location; see Place);
//   - its parent has no place either (including location: house, which is a
//     coverage scope and not a place), so PlaceOf would give "" too;
//   - part_of names no device, names itself, or names a device that is itself
//     part_of something. PlaceOf gives "" for all of those, and devices.Check
//     already reports them, so a second line would be noise.
//
// It describes the DOCUMENT, not the running state. The store keeps the last known
// room when a republish drops it, so after such a republish /state can still show a
// room for a probe this reports as declaring none. Both are true: the record no
// longer declares one, and statehouse has not forgotten the last one. The fix is
// the same either way: declare the room in the namespace.
//
// Like devices.Check it is advisory: it never changes the map and never refuses a
// device. Problems are in device-id order.
func PartOfRoomNotInheritedProblems(m map[string]DeviceConfig) []devices.Problem {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var problems []devices.Problem
	for _, id := range ids {
		d := m[id]
		if d.PartOf == "" || d.Place() != "" {
			continue
		}
		// A self-reference needs no clause of its own: parent is then d, which
		// has a part_of, so the chain test below skips it.
		parent, ok := m[d.PartOf]
		if !ok || parent.PartOf != "" || parent.Place() == "" {
			continue
		}
		problems = append(problems, devices.Problem{
			ID:      id,
			Field:   "room",
			Code:    CodePartOfRoomNotInherited,
			Message: notInheritedMessage(d.PartOf, parent),
		})
	}
	return problems
}

// notInheritedMessage tells the operator what to add. Only a real room id is ever
// suggested as a value; a parent that still has only a legacy location is sent to be
// migrated first.
func notInheritedMessage(parentID string, parent DeviceConfig) string {
	if parent.Room == "" {
		return fmt.Sprintf(
			"declares no room; its part_of %q has only the deprecated location %q, which is not a room id and is not inherited: give %q a room, then declare the same room here",
			parentID, parent.Location, parentID)
	}
	add := fmt.Sprintf("%q: %q", "room", parent.Room)
	if parent.Floor != "" {
		add += fmt.Sprintf(", %q: %q", "floor", parent.Floor)
	}
	return fmt.Sprintf(
		"declares no room; its part_of %q is in %q, but statehouse does not inherit room through part_of: add %s",
		parentID, parent.Room, add)
}

// DeviceCheck is an advisory check over a whole devices document. It reports
// problems and never changes m. devices.Check is one; statehouse's own policies
// that the shared vocabulary does not share are others.
type DeviceCheck func(m map[string]DeviceConfig) []devices.Problem

// DefaultDeviceChecks are the checks a Fetcher runs when its DeviceChecks is nil:
// the shared vocabulary's, then statehouse's own. A new statehouse-only advisory is
// one entry here plus its own tests; applyDevices does not change.
var DefaultDeviceChecks = []DeviceCheck{
	devices.Check,
	PartOfRoomNotInheritedProblems,
}

// runDeviceChecks runs checks over m and returns their problems, with first (the
// decoder's) merged in, sorted by device id then code. Sorting the combined list,
// rather than trusting each source's order, keeps one device's lines together and
// makes the log read the same on every reload. The sort is stable, so problems that
// tie keep their source order.
func runDeviceChecks(m map[string]DeviceConfig, checks []DeviceCheck, first []devices.Problem) []devices.Problem {
	problems := append([]devices.Problem(nil), first...)
	for _, check := range checks {
		problems = append(problems, check(m)...)
	}
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].ID != problems[j].ID {
			return problems[i].ID < problems[j].ID
		}
		return problems[i].Code < problems[j].Code
	})
	return problems
}
