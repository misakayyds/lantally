import React, { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { SettingsResponse } from "../api/types";
import { useApp } from "../state/AppContext";
import { downloadBlob } from "../utils/format";

export default function Settings() {
  const { lang, setLang, t, refreshTick, showToast } = useApp();
  const [settings, setSettings] = useState<SettingsResponse | null>(null);
  const [currentPwd, setCurrentPwd] = useState("");
  const [newPwd, setNewPwd] = useState("");
  const [confirmPwd, setConfirmPwd] = useState("");
  const [pwdError, setPwdError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await api.settings();
      setSettings(res);
    } catch {
      // handled by global 401 handler if expired
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  const handlePasswordChange = async (e: React.FormEvent) => {
    e.preventDefault();
    setPwdError(null);
    if (newPwd.trim().length < 8) {
      setPwdError(t.passwordMinLen);
      return;
    }
    if (newPwd !== confirmPwd) {
      setPwdError(t.passwordMismatch);
      return;
    }
    try {
      await api.changePassword(currentPwd, newPwd);
      setCurrentPwd("");
      setNewPwd("");
      setConfirmPwd("");
      showToast(t.saved);
    } catch (err) {
      setPwdError(err instanceof Error ? err.message : String(err));
    }
  };

  const handleBackup = async () => {
    try {
      const blob = await api.downloadBackup();
      downloadBlob(blob, "lantally.db");
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <section id="settings" className="page-stack">
      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.settingsInfoTitle}</h3>
            <p className="card-sub">
              {lang === "zh-CN"
                ? "版本、协议、样本保留策略与界面语言"
                : "Version, protocol, retention policy, and UI language"}
            </p>
          </div>
          <div className="chip-group">
            <button
              type="button"
              className={`chip-btn${lang === "zh-CN" ? " active" : ""}`}
              onClick={() => setLang("zh-CN")}
            >
              简体中文
            </button>
            <button
              type="button"
              className={`chip-btn${lang === "en" ? " active" : ""}`}
              onClick={() => setLang("en")}
            >
              English
            </button>
          </div>
        </div>
        <div className="settings-kv-grid">
          <div className="kv-box">
            <span>{lang === "zh-CN" ? "应用版本" : "App Version"}</span>
            <strong className="mono">{settings?.app_version || "-"}</strong>
          </div>
          <div className="kv-box">
            <span>{lang === "zh-CN" ? "上报协议版本" : "Protocol Version"}</span>
            <strong className="mono">v{settings?.protocol_version ?? 1}</strong>
          </div>
          <div className="kv-box">
            <span>{lang === "zh-CN" ? "明细样本保留" : "Sample Retention"}</span>
            <strong className="mono">{settings?.retention?.samples_days ?? 14} days</strong>
          </div>
          <div className="kv-box">
            <span>{lang === "zh-CN" ? "每日汇总保留" : "Daily Rollups"}</span>
            <strong className="mono">{settings?.retention?.daily || "permanent"}</strong>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.settingsPasswordTitle}</h3>
          </div>
        </div>
        <form className="device-actions" onSubmit={handlePasswordChange}>
          <div className="field">
            <label>{lang === "zh-CN" ? "当前密码" : "Current Password"}</label>
            <input
              type="password"
              required
              value={currentPwd}
              onChange={(e) => setCurrentPwd(e.target.value)}
            />
          </div>
          <div className="field">
            <label>{lang === "zh-CN" ? "新密码 (至少 8 位)" : "New Password (min 8 chars)"}</label>
            <input
              type="password"
              required
              minLength={8}
              value={newPwd}
              onChange={(e) => setNewPwd(e.target.value)}
            />
          </div>
          <div className="field">
            <label>{lang === "zh-CN" ? "确认新密码" : "Confirm New Password"}</label>
            <input
              type="password"
              required
              minLength={8}
              value={confirmPwd}
              onChange={(e) => setConfirmPwd(e.target.value)}
            />
          </div>
          <button type="submit" className="btn btn-primary btn-sm">
            {t.save}
          </button>
        </form>
        {pwdError && (
          <p className="field-error" style={{ padding: "0 1.15rem 1rem" }} role="alert">
            {pwdError}
          </p>
        )}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.settingsBackupTitle}</h3>
            <p className="card-sub">
              {lang === "zh-CN"
                ? "导出包含设备、规则、配额与流量账本的完整 SQLite 快照"
                : "Download a consistent SQLite snapshot containing all ledgers, rules, and devices"}
            </p>
          </div>
          <button type="button" className="btn btn-secondary btn-sm" onClick={handleBackup}>
            {t.downloadBackup}
          </button>
        </div>
      </div>
    </section>
  );
}
