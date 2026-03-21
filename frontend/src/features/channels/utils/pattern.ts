/**
 * Mirrors the backend xregexp matching logic in internal/pkg/xregexp/match.go.
 *
 * Rules:
 * 1. If the pattern contains no regex special chars, do an exact string comparison.
 * 2. Otherwise, wrap the pattern with ^ / $ anchors (unless already present),
 *    then apply it as a regex — matching the full model name.
 */

// Characters that indicate a regex pattern (must stay in sync with backend containsRegexChars).
const REGEX_SPECIAL_CHARS_RE = /[*?+[\]{}()^$.|\\]/;

function containsRegexChars(pattern: string): boolean {
  return REGEX_SPECIAL_CHARS_RE.test(pattern);
}

/**
 * Adds ^ prefix and $ suffix if not already present (accounting for common inline
 * modifier groups like (?i), (?m), (?s) that may precede the anchor).
 */
function ensureAnchored(pattern: string): string {
  // Detect leading ^ possibly preceded by inline modifier groups, e.g. (?i)^
  const hasStartAnchor = pattern.startsWith('^') || /^\(\?[a-z]+\)\^/.test(pattern);
  const hasEndAnchor = pattern.endsWith('$');

  if (!hasStartAnchor) pattern = '^' + pattern;
  if (!hasEndAnchor) pattern = pattern + '$';

  return pattern;
}

/**
 * Returns true if `model` matches `pattern` using the same rules as the backend.
 */
export function matchesModelPattern(model: string, pattern: string): boolean {
  if (!pattern) return true;

  if (!containsRegexChars(pattern)) {
    return model === pattern;
  }

  try {
    return new RegExp(ensureAnchored(pattern)).test(model);
  } catch {
    return false;
  }
}

/**
 * Filters `models` by `pattern` using the same rules as the backend Filter() function.
 * Returns an empty array when pattern is empty (mirrors backend behaviour).
 */
export function filterModelsByPattern(models: string[], pattern: string): string[] {
  if (!pattern) return [];
  return models.filter((model) => matchesModelPattern(model, pattern));
}

/**
 * Returns true when `search` contains regex special characters, indicating
 * that the user intends it to be treated as a regex expression.
 */
export function isRegexSearch(search: string): boolean {
  return !!search && REGEX_SPECIAL_CHARS_RE.test(search);
}

/**
 * Returns true when `search` looks like a regex but is syntactically invalid.
 */
export function isInvalidRegexSearch(search: string): boolean {
  if (!search || !REGEX_SPECIAL_CHARS_RE.test(search)) return false;
  try {
    new RegExp(search);
    return false;
  } catch {
    return true;
  }
}

/**
 * Search-oriented matching that supports both plain-text and regex searches.
 *
 * Rules:
 * 1. If `search` contains no regex special chars → case-insensitive substring match.
 * 2. If `search` contains regex special chars → treat as a regex (no forced anchors)
 *    and test case-insensitively against the model name.
 * 3. If `search` is an invalid regex → returns false.
 */
export function matchesSearchPattern(model: string, search: string): boolean {
  if (!search) return true;

  if (!REGEX_SPECIAL_CHARS_RE.test(search)) {
    return model.toLowerCase().includes(search.toLowerCase());
  }

  try {
    return new RegExp(search, 'i').test(model);
  } catch {
    return false;
  }
}
