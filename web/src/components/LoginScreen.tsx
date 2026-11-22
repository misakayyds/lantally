import React, { useState } from "react";
import { api, ApiError } from "../api/client";
import { useApp } from "../state/AppContext";

interface LoginScreenProps {
  setupNeeded: boolean;
  onAuthenticated: () => void;
}

export function LoginScreen({ setupNeeded, onAuthenticated }: LoginScreenProps) {
  const { t, lang, setLang } = useApp();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    const trimmed = password.trim();
    if (trimmed.length < 8) {
      setError(t.passwordMinLen);
      return;
    }
    if (setupNeeded && password !== confirm) {
      setError(t.passwordMismatch);
      return;
    }
    setSubmitting(true);
    try {
      if (setupNeeded) {
        await api.setup(password);
      } else {
        await api.login(password);
      }
      setPassword("");
      setConfirm("");
      onAuthenticated();
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(String(err));
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="login-layout">
      <aside className="login-aside">
        <div className="login-aside-inner">
          <p className="login-kicker">{t.kicker}</p>
          <h1>{t.heroTitle}</h1>
          <p className="login-lead">{t.heroLead}</p>
          <ul className="login-points">
            {t.heroPoints.map((pt) => (
              <li key={pt}>{pt}</li>
            ))}
          </ul>
        </div>
        <div className="login-grid" aria-hidden="true" />
      </aside>
      <main className="login-main">
        <form className="login-form" onSubmit={handleSubmit}>
          <div className="login-form-head">
            <span className="logo-mark">LT</span>
            <div style={{ flex: 1 }}>
              <strong>LanTally</strong>
              <span>{setupNeeded ? t.setupTitle : t.loginTitle}</span>
            </div>
            <button
              type="button"
              className="btn btn-text btn-sm"
              onClick={() => setLang(lang === "zh-CN" ? "en" : "zh-CN")}
            >
              {lang === "zh-CN" ? "EN" : "中文"}
            </button>
          </div>
          <div className="field">
            <label htmlFor="login-password">{t.adminPassword}</label>
            <input
              id="login-password"
              type="password"
              autoComplete={setupNeeded ? "new-password" : "current-password"}
              placeholder={t.passwordPlaceholder}
              required
              minLength={8}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          {setupNeeded && (
            <div className="field" style={{ marginTop: "0.75rem" }}>
              <label htmlFor="login-password2">{t.confirmPassword}</label>
              <input
                id="login-password2"
                type="password"
                autoComplete="new-password"
                placeholder={t.confirmPlaceholder}
                required
                minLength={8}
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
              />
            </div>
          )}
          {error && (
            <p className="field-error" role="alert">
              {error}
            </p>
          )}
          <button
            type="submit"
            className="btn btn-primary"
            style={{ marginTop: "0.95rem" }}
            disabled={submitting}
          >
            {setupNeeded ? t.createAndEnter : t.enter}
          </button>
        </form>
      </main>
    </div>
  );
}
