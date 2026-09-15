import { useEffect, useState } from "react";
import { ApiError, isSessionRecoveryError, loadWeeklyRhythm, saveWeeklyRhythm, type WeeklyRhythm } from "./api";
import { defaultWeeklyTargets, parseWeeklyTargets, type WeeklyTargets } from "./weekly-rhythm";

function validated(value: WeeklyRhythm): WeeklyRhythm {
  const targets = parseWeeklyTargets(value.targets);
  if (!targets || !Number.isInteger(value.revision) || value.revision < 0) throw new Error("Invalid weekly rhythm");
  return { revision: value.revision, targets };
}

export function useWeeklyRhythm(csrfToken: string, onSessionExpired: () => void) {
  const [rhythm, setRhythm] = useState<WeeklyRhythm>({ revision: 0, targets: defaultWeeklyTargets });
  const [loaded, setLoaded] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let active = true;
    loadWeeklyRhythm().then((value) => {
      const next = validated(value);
      if (active) { setRhythm(next); setLoaded(true); setError(""); }
    }).catch((failure) => {
      if (!active) return;
      if (isSessionRecoveryError(failure)) onSessionExpired();
      setError("Your weekly targets could not be loaded. Showing the last available targets.");
    });
    return () => { active = false; };
  }, [reload, onSessionExpired]);

  async function save(targets: WeeklyTargets) {
    if (!loaded || pending) return false;
    setPending(true);
    setError("");
    try {
      setRhythm(validated(await saveWeeklyRhythm({ revision: rhythm.revision, targets }, csrfToken)));
      return true;
    } catch (failure) {
      if (isSessionRecoveryError(failure)) onSessionExpired();
      if (failure instanceof ApiError && failure.status === 409) {
        setLoaded(false);
        setError("Your rhythm changed elsewhere. Reload targets before editing again.");
      } else setError("Your targets could not be saved. Retry saving your changes.");
      return false;
    } finally { setPending(false); }
  }

  return { targets: rhythm.targets, loaded, pending, error, save, retry: () => { setLoaded(false); setReload((value) => value + 1); } };
}
