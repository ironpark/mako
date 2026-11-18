package plugin

// openNonblock is nothing on Windows, whose opens do not wait on a pipe's
// writer.
const openNonblock = 0
