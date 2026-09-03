{{include:delegated-task-preamble.md}}

# Blind A/B screenshot judgement

Under `{{PAIRS_DIR}}` each subdirectory is one pair: `A.png` and `B.png` are
two captures of the same screen. You are told nothing else on purpose: not
which is older, not what changed, not what the task was, not who made them.
Do not look for that information. Read nothing but those two images per
pair, and run no command other than listing that directory.

For each pair, open both images and decide which screen is better for the
person using it: clearer, more complete, more consistent, fewer visible
defects (clipping, overlap, misalignment, missing or broken state, unreadable
or truncated text, placeholder content). Judge what is on the screen, not
what you imagine it was meant to become. Say `neither` only when you can find
no visible difference worth a preference, and say so with `high` confidence
only when the two images look identical to you.

Write to `{{VERDICT_FILE}}` with a shell redirect (the editing tools are
disabled for you): exactly one line per pair, nothing else, and print the
same lines as your response. `reason` names the concrete visible difference
that decided it, in one line.
AB_VERDICT: pair=<subdirectory> prefer=<A|B|neither> confidence=<low|med|high> reason=<one line>
