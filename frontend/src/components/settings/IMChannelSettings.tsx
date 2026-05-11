import { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useNodeClient } from '../../hooks/useNodeClient';
import { useToastStore } from '../../store/toast';
import type { RuntimeConfig, DingTalkConfig, FeishuConfig, WeChatBotConfig, WeComConfig } from '../../types/api';

const emptyDingTalk: DingTalkConfig = { enabled: false, app_key: '', app_secret: '', token: '', aes_key: '', agent_id: 0 };
// 飞书默认值与后端 FeishuConfig.Normalize 对齐：ack_emoji="Get"，renderer 未设字段走零值默认
// （disabled=false → 启用流式卡片；throttle_ms<=0 → 后端回退 300）。
// 注：飞书 reactions API 的 emoji_type 是 CamelCase（Get/Typing），早期误写的 GET/KEYBOARD
// 会被后端 Normalize 静默迁移，但新写入一律用 CamelCase。
const emptyFeishu: FeishuConfig = {
  enabled: false,
  app_id: '',
  app_secret: '',
  verification_token: '',
  encrypt_key: '',
  ack_emoji: 'Get',
  renderer: { disabled: false, throttle_ms: 300, show_agent_progress: false },
};
const emptyWeCom: WeComConfig = { enabled: false, corp_id: '', agent_id: 0, secret: '', token: '', encoding_aes_key: '' };
const emptyWeChatBot: WeChatBotConfig = { enabled: false };

// 老 DB 可能回来的 `GET`/`KEYBOARD` 直接 display 会变成 SelectRow 的"未知选项"留白。
// 前端同步后端迁移逻辑：把老值映射到 CamelCase 再渲染，保证 select 一定能命中一个选项。
function normalizeAckEmojiForDisplay(v?: string): string {
  if (v === 'GET') return 'Get';
  if (v === 'KEYBOARD') return 'Typing';
  if (v === 'Get' || v === 'Typing' || v === 'none') return v;
  return 'Get';
}

export function IMChannelSettings() {
  const { t } = useTranslation();
  const client = useNodeClient();
  const addToast = useToastStore((s) => s.addToast);

  const [dingtalk, setDingtalk] = useState<DingTalkConfig>(emptyDingTalk);
  const [feishu, setFeishu] = useState<FeishuConfig>(emptyFeishu);
  const [wecom, setWecom] = useState<WeComConfig>(emptyWeCom);
  const [wechatbot, setWechatbot] = useState<WeChatBotConfig>(emptyWeChatBot);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  const loadConfig = useCallback(async () => {
    setLoading(true);
    try {
      const cfg: RuntimeConfig = await client.getRuntimeConfig();
      if (cfg.channel) {
        if (cfg.channel.dingtalk) setDingtalk({ ...emptyDingTalk, ...cfg.channel.dingtalk });
        if (cfg.channel.feishu) setFeishu({ ...emptyFeishu, ...cfg.channel.feishu });
        if (cfg.channel.wecom) setWecom({ ...emptyWeCom, ...cfg.channel.wecom });
        setWechatbot({ enabled: cfg.channel.wechatbot?.enabled ?? false });
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : t('runtimeConfig.loadFailed');
      addToast('error', msg);
    } finally {
      setLoading(false);
    }
  }, [client, addToast, t]);

  useEffect(() => { loadConfig(); }, [loadConfig]);

  const handleSave = async () => {
    setSaving(true);
    try {
      // 1. 写入数据库并更新运行时配置
      await client.updateRuntimeConfig({
        channel: {
          dingtalk,
          feishu,
          wecom,
          wechatbot,
        },
      });
      // 2. 触发热重载（无需重启）
      await client.reloadChannels();
      addToast('success', t('runtimeConfig.applySuccess'));
    } catch (e) {
      const msg = e instanceof Error ? e.message : t('runtimeConfig.saveFailed');
      addToast('error', msg);
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12 text-[var(--text-secondary)]">
        {t('common.loading')}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <p className="text-xs text-[var(--text-secondary)]">
        {t('runtimeConfig.imChannelsHint')}
      </p>

      {/* 钉钉 */}
      <PlatformSection
        title={t('runtimeConfig.dingtalk')}
        enabled={dingtalk.enabled}
        onToggle={(v) => setDingtalk({ ...dingtalk, enabled: v })}
      >
        <FieldRow label={t('runtimeConfig.appKey')} value={dingtalk.app_key} onChange={(v) => setDingtalk({ ...dingtalk, app_key: v })} />
        <FieldRow label={t('runtimeConfig.appSecret')} value={dingtalk.app_secret} onChange={(v) => setDingtalk({ ...dingtalk, app_secret: v })} secret />
        <FieldRow label={t('runtimeConfig.token')} value={dingtalk.token} onChange={(v) => setDingtalk({ ...dingtalk, token: v })} />
        <FieldRow label={t('runtimeConfig.aesKey')} value={dingtalk.aes_key} onChange={(v) => setDingtalk({ ...dingtalk, aes_key: v })} secret />
        <FieldRow label={t('runtimeConfig.agentId')} value={String(dingtalk.agent_id || '')} onChange={(v) => setDingtalk({ ...dingtalk, agent_id: parseInt(v) || 0 })} />
      </PlatformSection>

      {/* 飞书 */}
      <PlatformSection
        title={t('runtimeConfig.feishu')}
        enabled={feishu.enabled}
        onToggle={(v) => setFeishu({ ...feishu, enabled: v })}
      >
        <FieldRow label={t('runtimeConfig.appId')} value={feishu.app_id} onChange={(v) => setFeishu({ ...feishu, app_id: v })} />
        <FieldRow label={t('runtimeConfig.appSecret')} value={feishu.app_secret} onChange={(v) => setFeishu({ ...feishu, app_secret: v })} secret />
        <FieldRow label={t('runtimeConfig.verificationToken')} value={feishu.verification_token} onChange={(v) => setFeishu({ ...feishu, verification_token: v })} />
        <FieldRow label={t('runtimeConfig.encryptKey')} value={feishu.encrypt_key} onChange={(v) => setFeishu({ ...feishu, encrypt_key: v })} secret />

        {/* 入口模式 —— Phase 0 CEO 决议:webhook XOR longconn,严禁同进程并存。
            longconn 入口不需公网/HTTPS,bot 主动连飞书 ws.open.feishu.cn:443。
            webhook 入口需公网 + HTTPS,飞书后台事件订阅地址填 /api/v1/channel/feishu/webhook。 */}
        <SelectRow
          label={t('runtimeConfig.feishuIngressMode')}
          hint={t('runtimeConfig.feishuIngressModeHint')}
          value={feishu.ingress_mode || ''}
          onChange={(v) => {
            const mode = v as '' | 'webhook' | 'longconn';
            setFeishu({
              ...feishu,
              ingress_mode: mode,
              // dual-ingress fatal guard:longconn 模式必须清空 webhook_url
              webhook_url: mode === 'longconn' ? '' : (feishu.webhook_url ?? ''),
              reliability: {
                ...(feishu.reliability || {}),
                longconn_enabled: mode === 'longconn',
              },
            });
          }}
          options={[
            { value: '', label: t('runtimeConfig.feishuIngressModeAuto') },
            { value: 'webhook', label: 'webhook' },
            { value: 'longconn', label: 'longconn' },
          ]}
        />

        {/* longconn gap fetch 子开关 —— 仅在 ingress=longconn 时显示 */}
        {feishu.ingress_mode === 'longconn' && (
          <ToggleRow
            label={t('runtimeConfig.feishuGapFetchEnabled')}
            hint={t('runtimeConfig.feishuGapFetchEnabledHint')}
            checked={feishu.reliability?.longconn_gap_fetch_enabled ?? false}
            onChange={(v) => setFeishu({
              ...feishu,
              reliability: { ...(feishu.reliability || {}), longconn_gap_fetch_enabled: v },
            })}
          />
        )}

        {/* webhook URL 声明 —— 仅 ingress=webhook 时显示。
            空 = 本进程不承载 webhook,与 longconn 兼容;非空 + longconn=true → Validate fatal。 */}
        {feishu.ingress_mode === 'webhook' && (
          <FieldRow
            label={t('runtimeConfig.feishuWebhookURL')}
            hint={t('runtimeConfig.feishuWebhookURLHint')}
            value={feishu.webhook_url ?? ''}
            onChange={(v) => setFeishu({ ...feishu, webhook_url: v })}
          />
        )}

        {/* Region —— cn 默认连 open.feishu.cn,intl/lark 切 open.larksuite.com */}
        <SelectRow
          label={t('runtimeConfig.feishuRegion')}
          hint={t('runtimeConfig.feishuRegionHint')}
          value={feishu.region || ''}
          onChange={(v) => setFeishu({ ...feishu, region: v })}
          options={[
            { value: '', label: t('runtimeConfig.feishuRegionCN') },
            { value: 'intl', label: t('runtimeConfig.feishuRegionIntl') },
          ]}
        />

        {/* Ack 表情 —— 对应后端 FeishuConfig.AckEmoji，合法值 Get/Typing/none（CamelCase，飞书 reactions API emoji_type）
            老值 GET/KEYBOARD 由后端 Normalize 静默迁移到 Get/Typing，此处 value 兜底用 Get。 */}
        <SelectRow
          label={t('runtimeConfig.feishuAckEmoji')}
          hint={t('runtimeConfig.feishuAckEmojiHint')}
          value={normalizeAckEmojiForDisplay(feishu.ack_emoji)}
          onChange={(v) => setFeishu({ ...feishu, ack_emoji: v })}
          options={[
            { value: 'Get', label: t('runtimeConfig.feishuAckEmojiGet') },
            { value: 'Typing', label: t('runtimeConfig.feishuAckEmojiTyping') },
            { value: 'none', label: t('runtimeConfig.feishuAckEmojiNone') },
          ]}
        />

        {/* 流式卡片（EventRenderer）子区块 —— UI 用"启用"正向语义，写库时翻转成 disabled 反向语义 */}
        <div className="pt-2 border-t border-[var(--border-color)]">
          <div className="text-xs font-medium text-[var(--text-primary)] pb-2">
            {t('runtimeConfig.feishuStreamingTitle')}
          </div>
          <ToggleRow
            label={t('runtimeConfig.feishuStreamingEnabled')}
            hint={t('runtimeConfig.feishuStreamingHint')}
            checked={!(feishu.renderer?.disabled ?? false)}
            onChange={(v) => setFeishu({
              ...feishu,
              renderer: { ...(feishu.renderer || {}), disabled: !v },
            })}
          />
          <FieldRow
            label={t('runtimeConfig.feishuThrottleMs')}
            hint={t('runtimeConfig.feishuThrottleHint')}
            value={String(feishu.renderer?.throttle_ms ?? 300)}
            onChange={(v) => {
              const n = parseInt(v, 10);
              setFeishu({
                ...feishu,
                renderer: { ...(feishu.renderer || {}), throttle_ms: Number.isFinite(n) ? n : 300 },
              });
            }}
          />
          <ToggleRow
            label={t('runtimeConfig.feishuShowAgentProgress')}
            hint={t('runtimeConfig.feishuShowAgentProgressHint')}
            checked={feishu.renderer?.show_agent_progress ?? false}
            onChange={(v) => setFeishu({
              ...feishu,
              renderer: { ...(feishu.renderer || {}), show_agent_progress: v },
            })}
          />
        </div>
      </PlatformSection>

      {/* 企业微信 */}
      <PlatformSection
        title={t('runtimeConfig.wecom')}
        enabled={wecom.enabled}
        onToggle={(v) => setWecom({ ...wecom, enabled: v })}
      >
        <FieldRow label={t('runtimeConfig.corpId')} value={wecom.corp_id} onChange={(v) => setWecom({ ...wecom, corp_id: v })} />
        <FieldRow label={t('runtimeConfig.agentId')} value={String(wecom.agent_id || '')} onChange={(v) => setWecom({ ...wecom, agent_id: parseInt(v) || 0 })} />
        <FieldRow label={t('runtimeConfig.secret')} value={wecom.secret} onChange={(v) => setWecom({ ...wecom, secret: v })} secret />
        <FieldRow label={t('runtimeConfig.token')} value={wecom.token} onChange={(v) => setWecom({ ...wecom, token: v })} />
        <FieldRow label={t('runtimeConfig.encodingAesKey')} value={wecom.encoding_aes_key} onChange={(v) => setWecom({ ...wecom, encoding_aes_key: v })} secret />
      </PlatformSection>

      {/* 官方微信 Bot */}
      <PlatformSection
        title={t('runtimeConfig.wechatbot')}
        enabled={wechatbot.enabled}
        onToggle={(v) => setWechatbot({ ...wechatbot, enabled: v })}
      >
        <p className="text-xs text-[var(--text-secondary)] leading-relaxed">
          {t('runtimeConfig.wechatbotHint')}
        </p>
      </PlatformSection>

      {/* 保存并热重载按钮 */}
      <button
        onClick={handleSave}
        disabled={saving}
        className="w-full px-4 py-2.5 text-sm font-medium text-white bg-[var(--accent-600)] hover:bg-[var(--accent-700)] disabled:opacity-50 rounded-xl transition-colors"
      >
        {saving ? t('common.loading') : t('runtimeConfig.apply')}
      </button>
    </div>
  );
}

/** 平台卡片 */
function PlatformSection({ title, enabled, onToggle, children }: {
  title: string; enabled: boolean; onToggle: (v: boolean) => void; children: React.ReactNode;
}) {
  const [expandedOverride, setExpandedOverride] = useState<boolean | null>(null);
  const expanded = expandedOverride ?? enabled;
  return (
    <div className="bg-[var(--bg-card)] border border-[var(--border-color)] rounded-2xl overflow-hidden shadow-sm">
      <div className="px-5 py-4 flex items-center justify-between border-b border-[var(--border-color)]">
        <div className="flex items-center gap-3">
          <span className="text-sm font-medium text-[var(--text-primary)]">{title}</span>
          <span className={`text-xs px-1.5 py-0.5 rounded ${enabled ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' : 'bg-[var(--bg-secondary)] text-[var(--text-secondary)]'}`}>
            {enabled ? 'ON' : 'OFF'}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <label className="relative inline-flex items-center cursor-pointer">
            <input type="checkbox" checked={enabled} onChange={(e) => onToggle(e.target.checked)} className="sr-only peer" />
            <div className="w-9 h-5 bg-[var(--bg-secondary)] peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-[var(--accent-600)]"></div>
          </label>
          <button onClick={() => setExpandedOverride(!expanded)} className="px-2 py-1 text-xs text-[var(--accent-600)] hover:text-[var(--accent-700)] dark:text-[var(--accent-300)]">
            {expanded ? '▲' : '▼'}
          </button>
        </div>
      </div>
      {expanded && <div className="p-5 space-y-4">{children}</div>}
    </div>
  );
}

/** 配置字段行。支持可选 hint（显示为 label 下方的次级灰字）。 */
function FieldRow({ label, value, onChange, secret, hint }: {
  label: string; value: string; onChange: (v: string) => void; secret?: boolean; hint?: string;
}) {
  const [visible, setVisible] = useState(false);

  return (
    <div className="flex items-start gap-3">
      <div className="w-40 shrink-0">
        <div className="text-sm text-[var(--text-secondary)]">{label}</div>
        {hint && <div className="text-[10px] text-[var(--text-tertiary)] leading-snug mt-0.5">{hint}</div>}
      </div>
      <div className="flex-1 relative">
        <input
          type={secret && !visible ? 'password' : 'text'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="w-full px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)] pr-8"
        />
        {secret && (
          <button
            type="button"
            onClick={() => setVisible(!visible)}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-[var(--text-secondary)] hover:text-[var(--text-primary)] transition-colors"
            tabIndex={-1}
          >
            {visible ? (
              <svg xmlns="http://www.w3.org/2000/svg" className="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94"/>
                <path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19"/>
                <line x1="1" y1="1" x2="23" y2="23"/>
              </svg>
            ) : (
              <svg xmlns="http://www.w3.org/2000/svg" className="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
                <circle cx="12" cy="12" r="3"/>
              </svg>
            )}
          </button>
        )}
      </div>
    </div>
  );
}

/** 下拉选择行，形态与 FieldRow 保持一致（label + hint + 控件） */
function SelectRow({ label, hint, value, options, onChange }: {
  label: string;
  hint?: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (v: string) => void;
}) {
  return (
    <div className="flex items-start gap-3">
      <div className="w-40 shrink-0">
        <div className="text-sm text-[var(--text-secondary)]">{label}</div>
        {hint && <div className="text-[10px] text-[var(--text-tertiary)] leading-snug mt-0.5">{hint}</div>}
      </div>
      <div className="flex-1">
        <select
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="w-full px-2 py-1.5 text-sm rounded-lg border border-[var(--border-color)] bg-[var(--bg-primary)] text-[var(--text-primary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-subtle)] focus:border-[var(--accent)]"
        >
          {options.map((opt) => (
            <option key={opt.value} value={opt.value}>{opt.label}</option>
          ))}
        </select>
      </div>
    </div>
  );
}

/** 开关行，与 PlatformSection 同款 toggle 样式，但嵌在表单内部 */
function ToggleRow({ label, hint, checked, onChange }: {
  label: string;
  hint?: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-3 py-1">
      <div className="w-40 shrink-0">
        <div className="text-sm text-[var(--text-secondary)]">{label}</div>
        {hint && <div className="text-[10px] text-[var(--text-tertiary)] leading-snug mt-0.5">{hint}</div>}
      </div>
      <div className="flex-1 flex items-center">
        <label className="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={checked}
            onChange={(e) => onChange(e.target.checked)}
            className="sr-only peer"
          />
          <div className="w-9 h-5 bg-[var(--bg-secondary)] peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-4 after:w-4 after:transition-all peer-checked:bg-[var(--accent-600)]"></div>
        </label>
      </div>
    </div>
  );
}
