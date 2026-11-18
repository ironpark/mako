package protocol

// TemplateSaved is the option a window allows an approval of kind
// ToolTemplate with once it has saved the template the approval's input
// holds, {"name", "content", "replace"}, in its own templates folder. A
// template is the window's, so the window that allows it saves it; an
// allow without this option, from a front-end that cannot save one,
// saves nothing.
const TemplateSaved = "saved"
