import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./bridge";
import type { GitInstallation } from "./types";

const unavailable: GitInstallation = {
  available: false,
  status: "unusable",
  platform: "",
  path: "",
  version: "",
  message: "Gitの利用状態を確認できませんでした。再確認してください。",
};

export function useGitInstallation(enabled: boolean) {
  const [installation, setInstallation] = useState<GitInstallation | null>(null);
  const [checking, setChecking] = useState(enabled);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(false);
  const inFlight = useRef<Promise<GitInstallation> | null>(null);

  const probe = useCallback(() => {
    // Reuse an outstanding startup probe during React StrictMode remounts.
    if (!inFlight.current) {
      inFlight.current = Promise.resolve().then(() => api.CheckGitInstallation())
        .finally(() => { inFlight.current = null; });
    }
    return inFlight.current;
  }, []);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  useEffect(() => {
    if (!enabled) return;
    let active = true;
    setChecking(true);
    probe().then((result) => {
      if (!active) return;
      setInstallation(result);
      setOpen(!result.available);
    }).catch((cause) => {
      if (!active) return;
      setInstallation(unavailable);
      setError(String(cause));
      setOpen(true);
    }).finally(() => { if (active) setChecking(false); });
    return () => { active = false; };
  }, [enabled, probe]);

  const retry = async () => {
    if (!enabled || checking) return false;
    setChecking(true);
    setError("");
    try {
      const result = await probe();
      if (!mounted.current) return false;
      setInstallation(result);
      if (result.available) setOpen(false);
      return result.available;
    } catch (cause) {
      if (mounted.current) setError(String(cause));
      return false;
    } finally {
      if (mounted.current) setChecking(false);
    }
  };

  const openGuide = async () => {
    setError("");
    try {
      await api.OpenGitInstallGuide();
    } catch (cause) {
      if (mounted.current) setError(String(cause));
    }
  };

  return {
    installation, checking, open, error, retry, openGuide,
    dismiss: () => setOpen(false),
    show: () => { setError(""); setOpen(true); },
  };
}
