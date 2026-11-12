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
// own but is part_of a device that does.
//
// In the shared vocabulary that record is fine: devices.PlaceOf hands the device its
// parent's room, so a probe "needn't repeat the fridge's room", and devices.Check
// says nothing. Statehouse deliberately does not call PlaceOf (#83). Room is relayed,
// never derived, and a probe declares its own room or has none. So exactly these
// records are the ones where statehouse and a PlaceOf-reading consumer answer
// differently, and an operator who wrote the namespace for PlaceOf would otherwise
// find the probe roomless with nothing saying why. This says why, and names the
// room to add.
//
// It reports nothing when the answers agree anyway:
//   - the device declares a place (room, or the legacy location; see Place);
//   - its parent has no place either, so PlaceOf would give "" too;
//   - part_of names no device, names itself, or names a device that is itself
//     part_of something. PlaceOf gives "" for all of those, and devices.Check
//     already reports them, so a second line would be noise.
//
// Like devices.Check it is advisory: it never changes the map and never refuses a
// device. Problems are in device-id order so the log reads the same on every reload.
func PartOfRoomNotInheritedProblems(m map[string]DeviceConfig) []devices.Problem {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var problems []devices.Problem
	for _, id := range ids {
		d := m[id]
		if d.PartOf == "" || d.PartOf == id || d.Place() != "" {
			continue
		}
		parent, ok := m[d.PartOf]
		if !ok || parent.PartOf != "" || parent.Place() == "" {
			continue
		}
		problems = append(problems, devices.Problem{
			ID:    id,
			Field: "room",
			Code:  CodePartOfRoomNotInherited,
			Message: fmt.Sprintf(
				"declares no room; its part_of %q is in %q, but statehouse does not inherit room through part_of: add \"room\": %q to give it one",
				d.PartOf, parent.Place(), parent.Place()),
		})
	}
	return problems
}
