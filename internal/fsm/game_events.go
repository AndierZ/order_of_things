package fsm

type Decision int

// 2. Create the enum values using a constant block
const (
	Unknown Decision = iota
	Cooperate
	Cheat
)

type Actor string

const (
	// Cooperator Always cooperate
	Cooperator Actor = "cooperator"
	// Flipper Always flips its own previous decision
	Flipper Actor = "flipper"
	// Retaliator If opponent cheat, will retaliate once with cheat
	Retaliator Actor = "retaliator"
	// CopyLeader Copies the last decision of the current leading actor
	CopyLeader Actor = "copy-leader"
)

type NewGame struct {
	Id     int64
	ActorA Actor
	ActorB Actor
}

type GameDecision struct {
	Actor    Actor
	Decision Decision
}
