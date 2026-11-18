package dev.ironpark.mako.workspace

// A project section's look, as the desktop's projectlook.go has it: the
// icon and colour the user picked from its icon, or, for a section with
// none picked, a shape and a colour its key draws, so it keeps the same
// one on every node.

/** The shapes a look drawn for a section is one of: a triangle, square, pentagon or hexagon, upright or turned 45°. */
val LOOK_SHAPES = listOf(
    "shape-triangle", "shape-triangle-45", "shape-square", "shape-square-45",
    "shape-pentagon", "shape-pentagon-45", "shape-hexagon", "shape-hexagon-45",
)

/** The icons a look offers, the shapes first. */
val LOOK_ICONS = LOOK_SHAPES + listOf(
    "folder", "package", "code-xml", "terminal", "globe", "book-open", "rocket", "flask-conical",
    "database", "gamepad-2", "star", "heart",
)

/** The colours a look offers, by name, as RGB; a drawn look is never the first, grey. */
val LOOK_COLORS = listOf(
    "gray" to 0x9aa0a6, "orange" to 0xe08a5f, "amber" to 0xd9b450, "green" to 0x5fbf7a,
    "teal" to 0x4fb8b0, "blue" to 0x6aa7ff, "purple" to 0xa993ff,
)

/** The look a seed draws: one of the shapes, in a colour but grey. */
internal fun drawnLook(seed: ULong): Look {
    val shapes = LOOK_SHAPES.size.toULong()
    val shape = LOOK_SHAPES[(seed % shapes).toInt()]
    val color = LOOK_COLORS[1 + (seed / shapes % (LOOK_COLORS.size - 1).toULong()).toInt()].first
    return Look(shape, color)
}

/** The look of section key: what was picked for it, filled in with what its key draws. */
fun lookOf(looks: Map<String, Look>, key: String): Look {
    val drawn = drawnLook(fnv64a(key))
    val own = looks[key] ?: return drawn
    return Look(
        own.icon.takeIf { it.isNotEmpty() && it in LOOK_ICONS } ?: drawn.icon,
        own.color.takeIf { c -> LOOK_COLORS.any { it.first == c } } ?: drawn.color,
    )
}

/** The RGB of the look colour called name; grey for none. */
fun lookRgb(name: String): Int = (LOOK_COLORS.firstOrNull { it.first == name } ?: LOOK_COLORS[0]).second
