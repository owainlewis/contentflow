import { useEffect, useRef, useState } from "react";
import { ApiError, isSessionRecoveryError, loadWeeklyRhythm, saveWeeklyRhythm, type WeeklyRhythm } from "./api";
import { defaultWeeklyTargets, parseWeeklyTargets } from "./weekly-rhythm";

function validated(value: WeeklyRhythm): WeeklyRhythm {
  const targets = parseWeeklyTargets(value.targets);
  if (!targets || !Number.isInteger(value.revision) || value.revision < 0) throw new Error("Invalid weekly rhythm");
  return { revision: value.revision, targets };
}

export function useWeeklyRhythm(csrfToken: string, onSessionExpired: () => void, enabled: boolean) {
  const [rhythm, setRhythm] = useState<WeeklyRhythm>({ revision: 0, targets: defaultWeeklyTargets });
  const [loaded, setLoaded] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [conflicted, setConflicted] = useState(false);
  const [reload, setReload] = useState(0);
  const readGeneration = useRef(0);

  useEffect(() => {
    if (!enabled) return;
    let active = true;
    const generation = ++readGeneration.current;
    loadWeeklyRhythm().then((value) => {
      const next = validated(value);
      if (active && generation === readGeneration.current) {
        setRhythm((current) => next.revision >= current.revision ? next : current);
        setLoaded(true);
        setError("");
        setConflicted(false);
      }
    }).catch((failure) => {
      if (!active || generation !== readGeneration.current) return;
      if (isSessionRecoveryError(failure)) onSessionExpired();
      setError("Your weekly targets could not be loaded. Showing the last available targets.");
    });
    return () => { active = false; };
  }, [reload, onSessionExpired, enabled]);

  async function save(draft: WeeklyRhythm) {
    if (!loaded || pending) return false;
    readGeneration.current++;
    setPending(true);
    setError("");
    try {
      const saved = validated(await saveWeeklyRhythm(draft, csrfToken));
      readGeneration.current++;
      setRhythm((current) => saved.revision >= current.revision ? saved : current);
      setLoaded(true);
      setConflicted(false);
      setError("");
      return true;
    } catch (failure) {
      readGeneration.current++;
      if (isSessionRecoveryError(failure)) onSessionExpired();
      if (failure instanceof ApiError && failure.status === 409) {
        setLoaded(false);
        setConflicted(true);
        setError("Your rhythm changed elsewhere. Reload targets before editing again.");
      } else setError("Your targets could not be saved. Retry saving your changes.");
      return false;
    } finally { setPending(false); }
  }

  return { ...rhythm, loaded, pending, error, conflicted, save, retry: () => { readGeneration.current++; setLoaded(false); setReload((value) => value + 1); } };
}
