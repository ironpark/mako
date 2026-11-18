package dev.ironpark.mako.workspace

import dev.ironpark.mako.Mesh
import dev.ironpark.mako.v2.Node
import dev.ironpark.mako.v2.WorkspaceState

// Whose workspace the app shows. A node shares the workspace only with the
// nodes it and they let each other in, and the phone lets no one in, so
// its own node holds none: the app shows a computer's instead, read from
// it through the phone's node (get_workspace with its node_id), and gives
// its edits to that computer (Workspace.to_node), which takes them from a
// node it lets in as from its own windows.

/**
 * ShownWorkspace is the workspace the app shows: node is the computer
 * holding it, null for this device's own node; editable says whether
 * that node takes the app's edits.
 */
data class ShownWorkspace(
    val state: WorkspaceState = WorkspaceState.getDefaultInstance(),
    val node: String? = null,
    val editable: Boolean = false,
)

/**
 * The computers whose workspace the app may show, in the order it tries
 * them: those that let this device in, or that it reaches through others
 * that do, new enough to hold the workspace as a CRDT, by node ID, which
 * does not change as names and links do.
 */
fun workspaceComputers(nodes: List<Node>): List<Node> =
    nodes.filter { (it.trustsUs || it.hops > 0) && it.revision >= Mesh.REVISION_WORKSPACE }.sortedBy { it.id }

/**
 * The workspace to show. This device's own, when its node holds one, as
 * it does once someone made it and a computer let each other in. Else
 * the first of computers, in order, that holds one; fetch reads a
 * computer's, null when it cannot be read. A computer shown before that
 * cannot be read now stays shown, as it was, rather than the list
 * changing its grouping each time a link drops. Several computers
 * normally hold one state, being one person's mesh that shares it; where
 * they differ, the first is shown, and edited, and its edits reach the
 * others as they share it. With none, the workspace is empty, and the
 * app lists as it did before workspaces.
 */
suspend fun chooseWorkspace(
    own: WorkspaceState,
    computers: List<Node>,
    shown: ShownWorkspace,
    fetch: suspend (String) -> WorkspaceState?,
): ShownWorkspace {
    if (own.serializedSize > 0) return ShownWorkspace(own, null, editable = true)
    for (c in computers) {
        val st = fetch(c.id)
        if (st == null && shown.node == c.id) return shown
        if (st != null && st.serializedSize > 0) {
            return ShownWorkspace(st, c.id, editable = c.revision >= Mesh.REVISION_PEER_WORKSPACE)
        }
    }
    return ShownWorkspace()
}
