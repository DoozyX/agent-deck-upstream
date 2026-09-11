package ui

// Goal preservation across an automatic context-budget handoff.
//
// The budget handler's job is continuity, and continuity has two halves: the
// state of the work, and the point of it. The state half was always carried —
// the outgoing agent writes PROMPT.md, and a transcript rebuild stands in when
// it does not. The point of the work was carried only by accident, in whatever
// the agent happened to mention, because nothing ever asked for it.
//
// That gap is the same one conductor rotation hit: a successor inherits files,
// not memory, and a handoff that records what was half-done but never what it
// was FOR produces a successor that finishes the wrong thing confidently.
// orchestrate's fix is a goal frozen on disk (goal.md) that rotation pastes
// into every successor's prompt. These two strings are the equivalent for
// every other session in the deck, which has no run directory and no skill
// telling it to keep one.
//
// Both are deliberately generic. requestWrap fires for every session the
// budget handler touches, so neither string may assume an orchestrate run, and
// neither resolves a path in Go — goal.md is named as the place to look, not
// computed. A session with no such file restates its goal from the task it was
// given, which is the common case and still worth writing down.

// wrapUpMessage is the instruction injected into a session that has hit its
// context budget, telling it what its continuation prompt must contain.
//
// It carries no shell sigil on purpose: this text is typed into an agent's
// prompt through tmux, where a stray "$" is at best noise and at worst mangled
// in transit.
func wrapUpMessage(promptPath string) string {
	return "Context budget reached. Finish and save your current work now, then write a continuation prompt " +
		"for a fresh session to " + promptPath + " (and any work notes alongside it). " +
		"Open that prompt with the goal of this work, restated verbatim rather than paraphrased, " +
		"plus the absolute path of any file the goal is frozen in — an orchestrate run keeps one at its " +
		"run directory's goal.md. Your successor inherits your files, not your memory, so a prompt that " +
		"records what was half-done but not what it was for leaves it to finish the wrong thing. " +
		"Do not start new work. When PROMPT.md is written, stop and wait."
}

// continuationSeed wraps a resolved handoff prompt in the framing the
// successor session is started on.
//
// The goal clause goes first, ahead of the handoff body, because the body can
// be up to DefaultHandoffMaxChars of prior conversation and an instruction
// behind that much text is an afterthought.
//
// It tells the successor to ask rather than infer, and that is not defensive
// boilerplate: when the outgoing agent never wrote PROMPT.md, the prompt below
// is rebuilt mechanically from a transcript tail, and a goal stated once at the
// start of a long session has scrolled out of that tail. It is genuinely not
// recoverable from what follows, so a confident guess is the failure mode to
// design against.
func continuationSeed(handoffText string) string {
	return "You are a continuation of a previous session that reached its context budget. " +
		"Before you resume: state the goal of this work in one sentence, and say where you got it. " +
		"Take it from the handoff below if the handoff states it; otherwise read the file the handoff " +
		"names — an orchestrate run freezes its goal in the run directory's goal.md. If neither states " +
		"it, ask. Do not infer the goal from work in progress: the prompt below may have been rebuilt " +
		"from a transcript tail, in which case the original goal is simply not in it, and a plausible " +
		"guess is the expensive kind of wrong.\n\n" +
		"Resume from this handoff prompt:\n\n" + handoffText
}
