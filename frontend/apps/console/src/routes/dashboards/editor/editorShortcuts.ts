// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The editor's keyboard shortcuts: undo (Ctrl/Cmd+Z), redo (Ctrl/Cmd+Shift+Z, and Ctrl+Y) and
// duplicate (Ctrl/Cmd+D). Copy and paste ride the browser's own copy and paste events instead, so
// they reach the system clipboard without asking for a permission.
//
// None of them fires while the author is typing: in a text field, Ctrl+Z belongs to the field
// (it undoes the typing), and taking it would undo an edit the author is not looking at.

export type EditorShortcut = 'undo' | 'redo' | 'duplicate';

// isTypingTarget says whether an event target is somewhere text is typed: an input, a text area, a
// select, an editable element, or a widget that says it is a text box or a combobox.
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  if (target.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"]')) return true;
  const role = target.closest('[role]')?.getAttribute('role');
  return role === 'textbox' || role === 'combobox' || role === 'searchbox' || role === 'spinbutton';
}

// editorShortcut names the editor shortcut a key press is, or null. A press inside a text field is
// never one.
export function editorShortcut(
  e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'altKey' | 'target'>,
): EditorShortcut | null {
  if (!(e.ctrlKey || e.metaKey) || e.altKey) return null;
  if (isTypingTarget(e.target)) return null;
  const key = e.key.toLowerCase();
  if (key === 'z') return e.shiftKey ? 'redo' : 'undo';
  if (key === 'y' && e.ctrlKey && !e.shiftKey) return 'redo';
  if (key === 'd' && !e.shiftKey) return 'duplicate';
  return null;
}
