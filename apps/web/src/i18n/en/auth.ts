// English mirror — auth domain copy (FR-179 login): brand title / form labels / buttons / errors + logout / session-expired copy.
export const auth = {
  login: {
    // Brand wordmark (side-by-side with lighthouse mark)
    title: 'Beacon',
    // Subtitle: one-line purpose
    subtitle: 'Sign in to manage the cross-server control plane',
    // Username field label & placeholder
    username: 'Username',
    usernamePlaceholder: 'Enter username',
    // Password field label & placeholder
    password: 'Password',
    passwordPlaceholder: 'Enter password',
    // Primary button: idle & submitting
    submit: 'Sign in',
    submitting: 'Signing in…',
    // Client validation: empty username or password
    missingCredentials: 'Enter username and password',
    // Fallback login failure (used for non-structured errors; structured errors prefer backend redacted message)
    failed: 'Sign-in failed. Please try again later',
    // Card footer environment hint
    envHint: 'Beacon cross-server control plane',
  },
  logout: {
    // Header logout button
    action: 'Sign out',
  },
  // Session expired (token invalid; kicked back to login — for future toast use)
  sessionExpired: 'Session expired. Please sign in again',
} as const
