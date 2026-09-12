import { useState } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { ApiError } from '../api/client';
import { createSession } from '../api/session';
import { useSession } from '../hooks/useSession';

/**
 * `/` -- the demo profile picker (phase 20, Approach F). Selecting a persona
 * IS the authentication: POST /api/session mints the HttpOnly cookie, the
 * refresh re-asks /api/me, and the authenticated state redirects to
 * /home (phase 52's dashboard; it used to be the bare run list at
 * /reports). Unauthenticated is a normal state here, not an error --
 * the picker is what unauthenticated looks like.
 */
export default function ProfilePicker() {
  const { loading, session, profiles, profilesError, refresh } = useSession();
  const [selecting, setSelecting] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  if (loading) {
    return <p className="text-sm text-slate-500">Loading…</p>;
  }

  if (session !== null) {
    return <Navigate to="/home" replace />;
  }

  const select = async (id: string) => {
    setSelecting(id);
    setError(null);
    try {
      await createSession(id);
      await refresh();
      // refresh() flips the session, which alone redirects via the Navigate
      // above; navigating explicitly keeps the contract local to the click
      // even if a future refresh change stops re-rendering this page.
      navigate('/home');
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'failed to select profile');
      setSelecting(null);
    }
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold text-slate-900 dark:text-slate-100">Who's operating?</h1>
        <p className="mt-1 text-sm text-slate-500">
          Selecting a profile signs you in as that persona. This is Honryu's demo mode: there is no password because
          selecting the persona is the authentication.
        </p>
      </div>

      {profilesError !== null && (
        <p className="text-sm text-red-600 dark:text-red-400" data-testid="profiles-error">
          {profilesError}
        </p>
      )}
      {error !== null && (
        <p className="text-sm text-red-600 dark:text-red-400" data-testid="select-error">
          {error}
        </p>
      )}

      {profiles.length === 0 ? (
        profilesError === null && <p className="text-sm text-slate-500">No profiles are configured.</p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {profiles.map((profile) => (
            <button
              key={profile.id}
              type="button"
              data-testid="profile-card"
              onClick={() => void select(profile.id)}
              disabled={selecting !== null}
              className="flex min-h-[44px] flex-col items-start gap-1 rounded-lg border border-slate-200 bg-white px-4 py-3 text-left transition-colors duration-200 hover:border-sky-400 hover:bg-sky-50 focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-sky-500 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-800 dark:bg-slate-900 dark:hover:border-sky-500 dark:hover:bg-sky-950/40"
            >
              <span className="text-sm font-medium text-slate-900 dark:text-slate-100">{profile.name}</span>
              <span className="font-mono text-xs text-slate-400">{profile.id}</span>
              {selecting === profile.id && <span className="text-xs text-sky-600 dark:text-sky-400">Signing in…</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
