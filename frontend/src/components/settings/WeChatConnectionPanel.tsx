import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import QRCode from 'qrcode';
import { LogOut, MessageCircle, QrCode, RefreshCw, RotateCcw, Smartphone, Wifi } from 'lucide-react';
import { useWechatConnection } from '../../hooks/useWechatConnection';
import { formatDateTime } from '../../utils/date';
import type { WeChatConnectionStatus, WeChatConversation } from '../../api/wechat';

const statusTone: Record<string, string> = {
  online: 'bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-900/20 dark:text-emerald-200 dark:border-emerald-800',
  waiting_qr_scan: 'bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-900/20 dark:text-blue-200 dark:border-blue-800',
  scanned: 'bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-900/20 dark:text-blue-200 dark:border-blue-800',
  recovering: 'bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-900/20 dark:text-amber-200 dark:border-amber-800',
  relogin_required: 'bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-900/20 dark:text-amber-200 dark:border-amber-800',
  error: 'bg-red-50 text-red-700 border-red-200 dark:bg-red-900/20 dark:text-red-200 dark:border-red-800',
  disabled: 'bg-[var(--bg-secondary)] text-[var(--text-secondary)] border-[var(--border-color)]',
};

export function WeChatConnectionPanel() {
  const { t } = useTranslation();
  const {
    status,
    conversations,
    qrUrl,
    lastEvent,
    loading,
    actionLoading,
    streamConnected,
    error,
    refresh,
    login,
    relogin,
    logout,
  } = useWechatConnection();

  const disabled = !status?.enabled || loading || actionLoading != null;
  const isOnline = status?.status === 'online';
  const needsLogin = status?.status === 'not_connected' || status?.status === 'offline' || status?.status === 'error';
  const shouldShowQR = Boolean(qrUrl) && status?.status !== 'online';

  return (
    <section className="bg-[var(--bg-card)] border border-[var(--border-color)] rounded-2xl shadow-sm overflow-hidden">
      <div className="flex flex-col gap-4 px-5 py-4 border-b border-[var(--border-color)] sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-3">
          <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-[var(--accent-subtle)] text-[var(--accent-600)]">
            <MessageCircle className="h-5 w-5" />
          </div>
          <div>
            <h3 className="text-sm font-semibold text-[var(--text-primary)] font-display">
              {t('wechatConnection.title', '微信 Bot 连接')}
            </h3>
            <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
              {t('wechatConnection.subtitle', '使用官方 wechatbot.dev 连接 iLink Bot。请让对方给微信里的 clawbot 发消息。')}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status={status} loading={loading} />
          <button
            type="button"
            onClick={() => refresh()}
            disabled={loading || actionLoading != null}
            className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-secondary)] hover:text-[var(--text-primary)] disabled:opacity-50"
          >
            <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
            {t('common.refresh', '刷新')}
          </button>
        </div>
      </div>

      <div className="grid gap-0 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="space-y-5 p-5">
          {!status?.enabled && !loading && (
            <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900/60 dark:bg-amber-900/20 dark:text-amber-200">
              {t('wechatConnection.disabledHint', '官方微信通道未启用，请先让管理员开启 channel.wechatbot.enabled。')}
            </div>
          )}

          {error && (
            <div className="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-900/20 dark:text-red-200">
              {error}
            </div>
          )}

          <div className="grid gap-3 sm:grid-cols-3">
            <Metric
              label={t('wechatConnection.account', 'Bot 账号')}
              value={status?.owner_account_id || t('wechatConnection.unbound', '未绑定')}
            />
            <Metric
              label={t('wechatConnection.conversations', '会话')}
              value={String(status?.conversation_count ?? conversations.length)}
            />
            <Metric
              label={t('wechatConnection.eventStream', '事件流')}
              value={streamConnected ? t('common.connected', '已连接') : t('common.disconnected', '已断开')}
            />
          </div>

          <div className="flex flex-wrap gap-2">
            {needsLogin ? (
              <button type="button" onClick={login} disabled={disabled} className={primaryButtonClass}>
                <Smartphone className="h-4 w-4" />
                {actionLoading === 'login' ? t('wechatConnection.connecting', '连接中...') : t('wechatConnection.connect', '连接微信 Bot')}
              </button>
            ) : (
              <button type="button" onClick={relogin} disabled={disabled} className={secondaryButtonClass}>
                <RotateCcw className="h-4 w-4" />
                {actionLoading === 'relogin' ? t('wechatConnection.reconnecting', '重登中...') : t('wechatConnection.relogin', '重新登录')}
              </button>
            )}
            <button type="button" onClick={logout} disabled={!isOnline || disabled} className={dangerButtonClass}>
              <LogOut className="h-4 w-4" />
              {actionLoading === 'logout' ? t('wechatConnection.disconnecting', '断开中...') : t('wechatConnection.logout', '断开连接')}
            </button>
          </div>

          {shouldShowQR && <QRCodeBlock qrUrl={qrUrl} />}
        </div>

        <div className="border-t border-[var(--border-color)] p-5 lg:border-l lg:border-t-0">
          <div className="mb-3 flex items-center justify-between">
            <h4 className="text-sm font-medium text-[var(--text-primary)]">
              {t('wechatConnection.recentConversations', '最近微信联系人')}
            </h4>
            {lastEvent?.created_at && (
              <span className="text-[11px] text-[var(--text-secondary)]">
                {formatDateTime(lastEvent.created_at)}
              </span>
            )}
          </div>
          {conversations.length === 0 ? (
            <div className="rounded-xl border border-dashed border-[var(--border-color)] px-4 py-8 text-center">
              <Wifi className="mx-auto mb-3 h-7 w-7 text-[var(--text-secondary)]" />
              <p className="text-sm text-[var(--text-primary)]">
                {t('wechatConnection.noConversations', '暂无微信会话')}
              </p>
              <p className="mt-1 text-xs text-[var(--text-secondary)]">
                {t('wechatConnection.noConversationsHint', '请让对方给微信里的 clawbot 发消息，系统会在这里显示最近联系人状态。')}
              </p>
            </div>
          ) : (
            <div className="space-y-2">
              {conversations.slice(0, 6).map((conversation) => (
                <ConversationRow key={conversation.peer_wxid} conversation={conversation} />
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function StatusBadge({ status, loading }: { status: WeChatConnectionStatus | null; loading: boolean }) {
  const { t } = useTranslation();
  const value = loading ? 'loading' : status?.status ?? 'not_connected';
  const tone = statusTone[value] ?? 'bg-[var(--bg-secondary)] text-[var(--text-secondary)] border-[var(--border-color)]';
  return (
    <span className={`inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium ${tone}`}>
      {t(`wechatConnection.status.${value}`, value)}
    </span>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-[var(--border-color)] bg-[var(--bg-primary)] px-4 py-3">
      <p className="text-xs text-[var(--text-secondary)]">{label}</p>
      <p className="mt-1 truncate font-mono text-sm text-[var(--text-primary)]">{value}</p>
    </div>
  );
}

function QRCodeBlock({ qrUrl }: { qrUrl: string }) {
  const { t } = useTranslation();
  const [generated, setGenerated] = useState({ source: '', imageSrc: '', error: '' });
  const imageSrc = qrUrl.startsWith('data:image/')
    ? qrUrl
    : generated.source === qrUrl ? generated.imageSrc : '';
  const renderError = generated.source === qrUrl ? generated.error : '';

  useEffect(() => {
    let cancelled = false;

    if (qrUrl.startsWith('data:image/')) {
      return () => {
        cancelled = true;
      };
    }

    QRCode.toDataURL(qrUrl, {
      errorCorrectionLevel: 'M',
      margin: 2,
      width: 240,
      color: {
        dark: '#111827',
        light: '#ffffff',
      },
    })
      .then((url) => {
        if (!cancelled) setGenerated({ source: qrUrl, imageSrc: url, error: '' });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setGenerated({
          source: qrUrl,
          imageSrc: '',
          error: err instanceof Error ? err.message : String(err),
        });
      });

    return () => {
      cancelled = true;
    };
  }, [qrUrl]);

  return (
    <div className="rounded-2xl border border-[var(--accent-border)] bg-[var(--accent-subtle)] p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-medium text-[var(--text-primary)]">
        <QrCode className="h-4 w-4 text-[var(--accent-600)]" />
        {t('wechatConnection.scanTitle', '使用微信扫码连接 Bot')}
      </div>
      <div className="flex flex-col gap-4 sm:flex-row">
        {imageSrc && (
          <div className="flex h-44 w-44 shrink-0 items-center justify-center rounded-xl border border-[var(--border-color)] bg-white p-2">
            <img src={imageSrc} alt={t('wechatConnection.qrAlt', '微信 Bot 连接二维码')} className="max-h-full max-w-full object-contain" />
          </div>
        )}
        {!imageSrc && (
          <div className="flex h-44 w-44 shrink-0 items-center justify-center rounded-xl border border-[var(--border-color)] bg-white p-4 text-center text-xs text-[var(--text-secondary)]">
            {renderError || t('wechatConnection.qrRendering', '二维码生成中...')}
          </div>
        )}
        <div className="min-w-0 flex-1">
          <p className="text-xs leading-5 text-[var(--text-secondary)]">
            {t('wechatConnection.scanHint', '扫码后会在微信里出现 clawbot。当前官方 SDK 只接收发给 clawbot 的消息，不会读取你微信号本人的普通私聊。')}
          </p>
          <div className="mt-3 break-all rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2 font-mono text-xs text-[var(--text-primary)]">
            {qrUrl}
          </div>
        </div>
      </div>
    </div>
  );
}

function ConversationRow({ conversation }: { conversation: WeChatConversation }) {
  const { t } = useTranslation();
  const name = conversation.peer_nickname || conversation.peer_wxid;
  const lastSeen = conversation.last_message_at
    ? t('wechatConnection.lastSeenAt', '最近互动 {{time}}', { time: formatDateTime(conversation.last_message_at) })
    : t('wechatConnection.contactStatusOnly', '仅显示联系人状态');
  return (
    <div className="flex items-center gap-3 rounded-xl border border-[var(--border-color)] px-3 py-2">
      {conversation.peer_avatar_url ? (
        <img src={conversation.peer_avatar_url} alt="" className="h-9 w-9 shrink-0 rounded-full object-cover" />
      ) : (
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-[var(--accent-subtle)] text-[var(--accent-600)]">
          <MessageCircle className="h-4 w-4" />
        </div>
      )}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <p className="truncate text-sm font-medium text-[var(--text-primary)]">{name}</p>
          <span className="shrink-0 rounded-full bg-[var(--bg-secondary)] px-2 py-0.5 text-[10px] text-[var(--text-secondary)]">
            {t('wechatConnection.statusOnly', '状态')}
          </span>
        </div>
        <p className="truncate text-xs text-[var(--text-secondary)]">
          {lastSeen}
        </p>
      </div>
    </div>
  );
}

const primaryButtonClass = 'inline-flex items-center gap-1.5 rounded-lg bg-[var(--accent-600)] px-3 py-2 text-sm font-medium text-white transition-colors hover:bg-[var(--accent-700)] disabled:cursor-not-allowed disabled:opacity-50';
const secondaryButtonClass = 'inline-flex items-center gap-1.5 rounded-lg border border-[var(--border-color)] px-3 py-2 text-sm font-medium text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-secondary)] disabled:cursor-not-allowed disabled:opacity-50';
const dangerButtonClass = 'inline-flex items-center gap-1.5 rounded-lg border border-red-200 px-3 py-2 text-sm font-medium text-red-600 transition-colors hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-red-900/60 dark:text-red-300 dark:hover:bg-red-900/20';
