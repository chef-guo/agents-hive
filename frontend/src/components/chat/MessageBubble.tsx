import { memo, useState, useMemo, useCallback, useEffect } from 'react';

// crypto.randomUUID 仅在 HTTPS / localhost（Secure Context）下可用
// HTTP 内网访问时降级为 Math.random 实现
function generateUUID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

async function loadAssetText(uri: string, sessionId?: string): Promise<string> {
  const params = new URLSearchParams({ uri, purpose: 'agent_artifact' });
  if (sessionId) params.set('session_id', sessionId);
  const headers: Record<string, string> = {};
  const token = localStorage.getItem('auth_token');
  if (token) headers.Authorization = `Bearer ${token}`;
  const resolveResp = await fetch(`/api/v1/assets/resolve?${params.toString()}`, { headers });
  if (!resolveResp.ok) {
    throw new Error('artifact resolve failed');
  }
  const resolved = await resolveResp.json() as { url: string };
  const assetResp = await fetch(resolved.url, { headers });
  if (!assetResp.ok) {
    throw new Error('artifact download failed');
  }
  return assetResp.text();
}
import { Streamdown } from 'streamdown';
import { ALLOWED_TAGS, STREAMDOWN_PLUGINS } from '../../utils/streamdownConfig';
import { useTranslation } from 'react-i18next';
import {
  Wrench, ChevronRight, ChevronDown,
  Copy, Check, ThumbsUp, ThumbsDown, RefreshCw,
  ExternalLink, Brain, FileText, Quote,
  ArrowUp, ArrowDown, X, AlertTriangle, Activity, ShieldAlert, PlayCircle,
} from 'lucide-react';
import type { Message } from '../../types/api';
import type { Artifact } from '../../store/canvas';
import { formatTimeOnly, isValidTimestamp } from '../../utils/date';
import { useCanvasStore } from '../../store/canvas';
import { AttachmentIcon } from './AttachmentIcon';
import { ClawIcon } from './shared';
import { formatFileSize } from './attachmentUtils';
import { useChatStore } from '../../store/chat';
import { getToolDisplayName } from '../../utils/toolName';
import { ArtifactCard } from './ArtifactCard';
import { ToolAdapter } from './ToolAdapter';
import { parseMessageContent, parseMessageContentWithSkeleton, hasOpenArtifact, mergeArtifactManifestSegments } from '../../utils/artifactParser';
import type { ArtifactType } from '../../store/canvas';
import { LocalNodeClient } from '../../api/node-client';

const assetClient = new LocalNodeClient();


function MonkeyIcon({ className = '' }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg">
      {/* 耳朵 */}
      <circle cx="3.5" cy="8.5" r="3" fill="rgba(255,255,255,0.7)"/>
      <circle cx="3.5" cy="8.5" r="1.6" fill="rgba(255,200,160,0.8)"/>
      <circle cx="16.5" cy="8.5" r="3" fill="rgba(255,255,255,0.7)"/>
      <circle cx="16.5" cy="8.5" r="1.6" fill="rgba(255,200,160,0.8)"/>
      {/* 头 */}
      <circle cx="10" cy="10" r="6.5" fill="rgba(255,255,255,0.85)"/>
      {/* 脸部 */}
      <ellipse cx="10" cy="12" rx="4" ry="3.2" fill="rgba(255,220,190,0.9)"/>
      {/* 眼睛 */}
      <circle cx="7.8" cy="8.8" r="0.9" fill="rgba(0,0,0,0.7)"/>
      <circle cx="12.2" cy="8.8" r="0.9" fill="rgba(0,0,0,0.7)"/>
      {/* 眼睛高光 */}
      <circle cx="8.1" cy="8.4" r="0.3" fill="white"/>
      <circle cx="12.5" cy="8.4" r="0.3" fill="white"/>
      {/* 鼻子 */}
      <ellipse cx="10" cy="11.5" rx="1.8" ry="1.2" fill="rgba(200,160,130,0.6)"/>
      <circle cx="9.2" cy="11.5" r="0.4" fill="rgba(0,0,0,0.5)"/>
      <circle cx="10.8" cy="11.5" r="0.4" fill="rgba(0,0,0,0.5)"/>
      {/* 嘴巴 */}
      <path d="M8.8 13.2 Q10 14.2 11.2 13.2" stroke="rgba(0,0,0,0.4)" strokeWidth="0.5" strokeLinecap="round" fill="none"/>
    </svg>
  );
}


interface Props {
  message: Message;
  showRole?: boolean;
  isLast?: boolean;
  onRegenerate?: () => void;
  toolResults?: Map<string, string>;
  toolErrors?: Map<string, boolean>; // tool_call_id → is_error
  toolRecoverable?: Map<string, boolean>; // tool_call_id → recoverable
  toolErrorKinds?: Map<string, string>; // tool_call_id → error_kind
  toolNames?: Map<string, string>; // tool_call_id → tool_name
  sessionId?: string;
  kbDomainId?: string;
}

export const MessageBubble = memo(function MessageBubble({
  message, showRole = true, isLast = false, onRegenerate, toolResults, toolErrors, toolRecoverable, toolErrorKinds, toolNames, sessionId, kbDomainId,
}: Props) {
  const { t } = useTranslation();
  const isUser = message.role === 'user';
  const isTool = message.role === 'tool';
  const isStreaming = useChatStore((s) => s.streaming);
  const streamingMessageId = useChatStore((s) => s.streamingMessageId);

  // 精确检测当前消息是否正在流式输出
  // 优先用 streamingMessageId 匹配，fallback 用空内容占位符检测
  const isThisMessageStreaming = isStreaming && (
    (streamingMessageId != null && message.timestamp === streamingMessageId) ||
    (message.content === '' && isStreaming)
  );

  const segments = useMemo(() => {
    if (!message.content || message.is_error) return null;
    const parsed = isThisMessageStreaming && hasOpenArtifact(message.content)
      ? parseMessageContentWithSkeleton(message.content)
      : parseMessageContent(message.content);
    return mergeArtifactManifestSegments(parsed, message.artifacts);
  }, [message.content, message.is_error, message.artifacts, isThisMessageStreaming]);

  const hasArtifacts = segments?.some((s) => s.type === 'artifact') ?? false;
  const toolCallsForRender = useMemo(() => {
    if (!message.tool_calls?.length) return [];
    const merged: NonNullable<Message['tool_calls']> = [];
    const indexByID = new Map<string, number>();
    for (const tc of message.tool_calls) {
      const existingIndex = indexByID.get(tc.id);
      if (existingIndex === undefined) {
        indexByID.set(tc.id, merged.length);
        merged.push(tc);
        continue;
      }
      const existing = merged[existingIndex];
      merged[existingIndex] = {
        ...existing,
        ...tc,
        name: tc.name || existing.name,
        arguments: tc.arguments.length >= existing.arguments.length ? tc.arguments : existing.arguments,
      };
    }
    return merged;
  }, [message.tool_calls]);

  const [copied, setCopied] = useState(false);
  const [liked, setLiked] = useState<'up' | 'down' | null>(null);
  const [reasoningExpanded, setReasoningExpanded] = useState(false);

  const handleCopy = useCallback(async () => {
    if (!message.content) return;
    await navigator.clipboard.writeText(message.content);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [message.content]);

  // --- 用户消息 ---
  if (isUser) {
    return (
      <div className={`msg-container group px-4 ${showRole ? 'pt-1 pb-0' : 'py-0'}`}>
        <div className="flex gap-3">
            {showRole ? (
              <div className="w-7 h-7 rounded-full bg-[var(--bg-card)] border border-[var(--border-color)] flex items-center justify-center shrink-0 mt-0.5 shadow-sm">
                <MonkeyIcon className="w-4 h-4" />
              </div>
            ) : (
              <div className="w-7 shrink-0" />
            )}
            <div className="flex flex-col items-start max-w-[85%] lg:max-w-[75%] min-w-0">
              <div className="bg-[var(--accent-600)] dark:bg-[var(--accent-500)] border-0 rounded-xl px-3 py-2 text-[13px] leading-[1.5] text-white shadow-sm" style={{ overflowWrap: 'anywhere', wordBreak: 'break-word' }}>
                <span className="whitespace-pre-wrap">{message.content}</span>
                {message.attachments && message.attachments.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {message.attachments.map((att, i) => <UserAttachmentPreview key={i} attachment={att} sessionId={sessionId} />)}
                  </div>
                )}
              </div>
              {/* 用户消息操作栏：桌面 hover 显示，移动端始终显示 */}
              <div className="msg-actions mt-1 flex items-center gap-0.5">
                <ActionBtn
                  icon={copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                  label={copied ? t('chat.copied') : t('chat.copy')}
                  onClick={handleCopy}
                  active={copied}
                />
                {isLast && onRegenerate && (
                  <ActionBtn
                    icon={<RefreshCw className="w-3.5 h-3.5" />}
                    label={t('chat.regenerate')}
                    onClick={onRegenerate}
                  />
                )}
              </div>
              {isValidTimestamp(message.timestamp) && (
                <span className="msg-timestamp text-[11px] text-[var(--text-secondary)] mt-0.5 mr-1">
                  {formatTimeOnly(message.timestamp!)}
                </span>
              )}
            </div>
          </div>
      </div>
    );
  }

  // --- 工具结果消息（role=tool）---
  if (isTool) {
    // 判断是否为错误结果：优先使用后端标记，兜底字符串匹配
    const isError = message.is_error
      || message.content.startsWith('tool error:')
      || message.content.startsWith('tool execution failed:')
      || (message.content.startsWith("tool '") && message.content.includes('not allowed'))
      || message.content === 'ToolBridge not initialized'
      || message.content.startsWith('[工具调用被中断')
      || message.content.startsWith('[工具执行失败');
    const resultStatus = isError ? 'error' : 'success';

    // 预览摘要：对图片结果特殊处理
    let preview: string;
    try {
      const parsed = JSON.parse(message.content);
      if (parsed?.message && parsed?.url) {
        preview = parsed.message;  // 显示 "图片已生成" 而不是 JSON
      } else {
        preview = message.content.length > 100
          ? message.content.slice(0, 100).replace(/\n/g, ' ') + '…'
          : message.content.replace(/\n/g, ' ');
      }
    } catch {
      preview = message.content.length > 100
        ? message.content.slice(0, 100).replace(/\n/g, ' ') + '…'
        : message.content.replace(/\n/g, ' ');
    }

    return (
      <div className={`msg-container group px-4 ${showRole ? 'pt-1 pb-0' : 'py-0'}`}>
        <div className="flex gap-3">
            <div className="w-8 shrink-0" />
            <div className="flex-1 min-w-0 w-0" style={{ overflow: 'hidden', minWidth: 0 }}>
              <ToolResultCard
                status={resultStatus}
                preview={preview}
                content={message.content}
                onCopy={handleCopy}
                copied={copied}
                copyLabel={copied ? t('chat.copied') : t('chat.copy')}
                toolName={message.tool_name || (message.tool_call_id ? toolNames?.get(message.tool_call_id) : undefined)}
              />
            </div>
          </div>
      </div>
    );
  }

  // --- 助手消息（气泡包裹） ---
  return (
    <div className={`msg-container group px-4 sm:px-4 max-sm:px-2 ${showRole ? 'pt-1 pb-0' : 'py-0'}`}>
      <div className="flex gap-3 max-sm:gap-2">
          {showRole ? (
            <div className="w-7 h-7 max-sm:w-6 max-sm:h-6 rounded-full bg-[var(--accent-600)] dark:bg-[var(--accent-500)] flex items-center justify-center shrink-0 mt-0.5">
              <ClawIcon className="w-4 h-4 max-sm:w-3.5 max-sm:h-3.5" />
            </div>
          ) : (
            <div className="w-7 max-sm:w-6 shrink-0" />
          )}
          <div className="flex-1 min-w-0 w-0" style={{ overflow: 'hidden', minWidth: 0 }}>
            {/* 思考过程卡片 */}
            {message.reasoning_content && (
              <div className="mb-3">
                <div className="bg-[var(--bg-card)] border border-[var(--border-color)] rounded-2xl overflow-hidden shadow-sm">
                  <button
                    onClick={() => setReasoningExpanded(!reasoningExpanded)}
                    className="w-full flex items-center gap-2 px-3 py-2 text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] transition-colors select-none"
                  >
                    <Brain className="w-3.5 h-3.5 text-[var(--accent-600)] dark:text-[var(--accent-300)] shrink-0" />
                    <span className="font-medium">{t('chat.showThought')}</span>
                    <ChevronRight className={`w-3.5 h-3.5 ml-auto transition-transform ${reasoningExpanded ? 'rotate-90' : ''}`} />
                  </button>
                  {reasoningExpanded && (
                    <div className="px-3 pb-3 border-t border-[var(--border-color)]">
                      <div className="mt-2 text-xs text-[var(--text-secondary)]">
                        <div className="prose prose-xs max-w-none dark:prose-invert prose-p:text-[var(--text-secondary)] prose-li:text-[var(--text-secondary)]">
                          <Streamdown
                            plugins={STREAMDOWN_PLUGINS}
                            allowedTags={ALLOWED_TAGS}
                          >
                            {message.reasoning_content}
                          </Streamdown>
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}
            {/* AI 消息内容：分段渲染（文本段 + artifact 卡片） */}
            {message.content && !message.is_error && segments && (
              <div className="flex flex-col gap-1">
                {segments.map((seg, i) =>
                  seg.type === 'text' ? (
                    <div key={i} className="ai-message-card">
                      <div className="message-content prose prose-sm dark:prose-invert prose-headings:text-[var(--text-primary)] prose-p:text-[var(--text-primary)] prose-li:text-[var(--text-primary)] prose-strong:text-[var(--text-primary)] prose-a:text-[var(--accent)] dark:prose-a:text-[var(--accent)] text-[var(--text-primary)] text-[13px] leading-[1.5]" style={{ maxWidth: '100%', overflow: 'hidden', overflowWrap: 'anywhere', wordBreak: 'break-word' }}>
                        <Streamdown
                          parseIncompleteMarkdown={isThisMessageStreaming}
                          plugins={STREAMDOWN_PLUGINS}
                          allowedTags={ALLOWED_TAGS}
                        >
                          {seg.content}
                        </Streamdown>
                      </div>
                    </div>
                  ) : (
                    <ArtifactCard
                      key={i}
                      title={seg.title!}
                      artifactType={seg.artifactType!}
                      content={seg.content}
                      language={seg.language}
                      isLoading={seg.isLoading}
                      onOpenCanvas={async () => {
                        const typeMap: Record<string, ArtifactType> = {
                          markdown: 'markdown', html: 'html', code: 'code', ppt: 'ppt',
                        };
                        const artifactStoreType = typeMap[seg.artifactType!] ?? 'markdown';
                        const lang = seg.language ?? (seg.artifactType === 'code' ? 'text' : seg.artifactType ?? 'markdown');
                        let content = seg.content;
                        if (seg.assetUri) {
                          try {
                            content = await loadAssetText(seg.assetUri, sessionId);
                          } catch {
                            content = seg.content;
                          }
                        }
                        useCanvasStore.getState().openArtifact({
                          id: generateUUID(),
                          title: seg.title!,
                          language: lang,
                          content,
                          assetUri: seg.assetUri,
                          type: artifactStoreType,
                        });
                      }}
                    />
                  )
                )}
              </div>
            )}
            {/* 错误消息：折叠卡片样式，与工具调用卡片一致 */}
            {message.content && message.is_error && (
              <ErrorCard content={message.content} />
            )}

            {toolCallsForRender.length > 0 && (
              <div className="mt-2.5">
                <ToolCallsSection
                  calls={toolCallsForRender}
                  toolResults={toolResults}
                  toolErrors={toolErrors}
                  toolRecoverable={toolRecoverable}
                  toolErrorKinds={toolErrorKinds}
                  sessionId={sessionId}
                  kbDomainId={kbDomainId}
                />
              </div>
            )}

            {message.citations && message.citations.length > 0 && (
              <CitationStrip citations={message.citations} />
            )}

            {/* 底部信息栏：token 用量 + 时长 | 操作按钮（错误消息隐藏） */}
            {/* 流式占位符（无 content / usage / duration / 有效时间戳）不渲染底栏，避免出现孤儿空行或 Invalid Date 残影 */}
            {!message.is_error && (message.content || message.usage || message.llm_duration !== undefined || isValidTimestamp(message.timestamp)) && (
            <div className="mt-2 flex items-center justify-between min-h-[28px]">
              {/* 左侧：token 用量 + 时长（始终可见） */}
              <div className="flex items-center gap-3">
                {message.usage && (
                  <span className="token-usage">
                    <ArrowUp className="w-3 h-3 text-[var(--accent)]" />
                    <span className="font-medium">{message.usage.input_tokens.toLocaleString()}</span>
                    <ArrowDown className="w-3 h-3 text-green-400" />
                    <span className="font-medium">{message.usage.output_tokens.toLocaleString()}</span>
                  </span>
                )}
                {message.llm_duration !== undefined && (
                  <span className="text-[11px] text-[var(--text-secondary)] font-medium">
                    {message.llm_duration >= 1000
                      ? `${(message.llm_duration / 1000).toFixed(1)}s`
                      : `${message.llm_duration}ms`}
                  </span>
                )}
                {isValidTimestamp(message.timestamp) && (
                  <span className="text-[11px] text-[var(--text-secondary)] opacity-60">
                    {formatTimeOnly(message.timestamp!)}
                  </span>
                )}
              </div>
              {/* 右侧：操作按钮 */}
              {message.content && (
                <div className="msg-actions flex items-center gap-0.5">
                  {/* hasArtifacts 时隐藏全消息预览按钮，每个 ArtifactCard 自带"查看全文" */}
                  {!hasArtifacts && (
                    <ActionBtn
                      icon={<FileText className="w-3.5 h-3.5" />}
                      label={t('chat.preview')}
                      onClick={() => {
                        const c = message.content!;
                        const html = isHtmlDocument(c);
                        const artifact: Artifact = {
                          id: generateUUID(),
                          title: t('chat.preview'),
                          language: html ? 'html' : 'markdown',
                          content: c,
                          type: html ? 'html' : 'markdown',
                        };
                        useCanvasStore.getState().openArtifact(artifact);
                      }}
                    />
                  )}
                  <ActionBtn
                    icon={copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                    label={copied ? t('chat.copied') : t('chat.copy')}
                    onClick={handleCopy}
                    active={copied}
                  />
                  <ActionBtn
                    icon={<ThumbsUp className="w-3.5 h-3.5" />}
                    label={t('chat.like')}
                    onClick={() => setLiked(liked === 'up' ? null : 'up')}
                    active={liked === 'up'}
                  />
                  <ActionBtn
                    icon={<ThumbsDown className="w-3.5 h-3.5" />}
                    label={t('chat.dislike')}
                    onClick={() => setLiked(liked === 'down' ? null : 'down')}
                    active={liked === 'down'}
                  />
                </div>
              )}
            </div>
            )}
          </div>
        </div>
    </div>
  );
});

function UserAttachmentPreview({ attachment, sessionId }: { attachment: NonNullable<Message['attachments']>[number]; sessionId?: string }) {
  const [url, setURL] = useState(attachment.data ? `data:${attachment.mime_type};base64,${attachment.data}` : '');

  useEffect(() => {
    if (url || !attachment.asset_uri) return;
    let cancelled = false;
    void assetClient.resolveAsset(attachment.asset_uri, { purpose: 'chat_attachment', sessionId })
      .then((res) => {
        if (!cancelled) setURL(res.url);
      })
      .catch(() => {
        if (!cancelled) setURL('');
      });
    return () => {
      cancelled = true;
    };
  }, [attachment.asset_uri, sessionId, url]);

  if (attachment.mime_type.startsWith('image/') && url) {
    return (
      <img
        src={url}
        alt={attachment.filename}
        className="max-h-32 rounded-lg"
      />
    );
  }

  return (
    <a
      href={url || undefined}
      target="_blank"
      rel="noreferrer"
      className="flex items-center gap-1.5 px-2 py-1 bg-[var(--bg-secondary)] rounded-lg text-xs text-[var(--text-secondary)]"
      onClick={(e) => {
        if (!url) e.preventDefault();
      }}
    >
      <AttachmentIcon mimeType={attachment.mime_type} />
      <span className="truncate max-w-[120px]">{attachment.filename}</span>
      <span>{formatFileSize(attachment.size)}</span>
    </a>
  );
}

function CitationStrip({ citations }: { citations: NonNullable<Message['citations']> }) {
  const visible = citations.slice(0, 4);
  const extra = citations.length - visible.length;
  return (
    <div className="mt-2.5 flex flex-wrap gap-1.5">
      {visible.map((citation, index) => {
        const token = citation.token || citation.Token || '';
        const docId = citation.document_id || citation.DocumentID || citation.doc_id || '';
        const nodeId = citation.node_id || citation.NodeID || '';
        const nodePath = citation.node_path || citation.NodePath || '';
        const startPage = citation.start_page || citation.StartPage;
        const endPage = citation.end_page || citation.EndPage;
        const version = citation.document_version || citation.DocumentVersion || '';
        const pageLabel = startPage && endPage
          ? startPage === endPage ? `p.${startPage}` : `p.${startPage}-${endPage}`
          : '';
        const title = citation.citation_text || citation.CitationText || nodePath || nodeId || docId || token || `引用 ${index + 1}`;
        const meta = [
          docId,
          nodePath || nodeId,
          pageLabel,
          version,
        ].filter(Boolean).join(' · ');
        return (
          <div
            key={`${token || docId || nodeId || index}`}
            className="inline-flex max-w-full items-center gap-1.5 rounded-lg border border-[var(--border-color)] bg-[var(--bg-card)] px-2 py-1 text-[11px] text-[var(--text-secondary)]"
            title={meta ? `${title}\n${meta}` : title}
          >
            <Quote className="h-3 w-3 shrink-0 text-[var(--accent-600)] dark:text-[var(--accent-300)]" aria-hidden="true" />
            <span className="max-w-[220px] truncate font-medium text-[var(--text-primary)]">{title}</span>
            {meta && <span className="max-w-[180px] truncate font-mono text-[10px] opacity-75">{meta}</span>}
            {(citation.verified || citation.Verified) && <span className="rounded-full bg-emerald-100 px-1.5 py-0.5 text-[10px] font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300">verified</span>}
          </div>
        );
      })}
      {extra > 0 && (
        <span className="inline-flex items-center rounded-lg border border-[var(--border-color)] px-2 py-1 text-[11px] text-[var(--text-secondary)]">
          +{extra}
        </span>
      )}
    </div>
  );
}

/* ===== 操作按钮 ===== */
function ActionBtn({ icon, label, onClick, active, danger }: {
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
  active?: boolean;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className={`action-btn relative p-1.5 rounded-full transition-all duration-150 ${
        active
          ? 'text-[var(--accent-600)] dark:text-[var(--accent-300)] bg-[var(--accent-50)] dark:bg-[var(--accent-light)]'
          : danger
            ? 'text-[var(--text-secondary)] hover:text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20'
            : 'text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)]'
      }`}
    >
      {icon}
      <span className="action-tooltip">{label}</span>
    </button>
  );
}


/* ===== 工具结果图标配色 =====
 * 品牌统一后去掉按工具分类的硬编码配色；颜色只表达语义（运行中/成功/错误）。
 * running + success → var(--accent-600)；error → var(--danger)。
 */
function getToolAccentByStatus(status?: 'running' | 'success' | 'error'): string {
  return status === 'error' ? 'var(--danger)' : 'var(--accent-600)';
}

type RenderToolCall = NonNullable<Message['tool_calls']>[number];

function getCompactToolSummary(calls: RenderToolCall[], toolResults?: Map<string, string>): string {
  const finished = calls.filter((tc) => toolResults?.has(tc.id));
  const pending = calls.length - finished.length;
  const names = Array.from(new Set(calls.map((tc) => tc.name).filter(Boolean))).slice(0, 3);
  const nameText = names.join(', ');
  const suffix = calls.length > names.length ? ` +${calls.length - names.length}` : '';

  if (pending > 0) {
    return `${nameText}${suffix} · ${finished.length}/${calls.length} completed`;
  }
  return `${calls.length} tool${calls.length === 1 ? '' : 's'} completed · ${nameText}${suffix}`;
}

function ToolCallsSection({
  calls,
  toolResults,
  toolErrors,
  toolRecoverable,
  toolErrorKinds,
  sessionId,
  kbDomainId,
}: {
  calls: RenderToolCall[];
  toolResults?: Map<string, string>;
  toolErrors?: Map<string, boolean>;
  toolRecoverable?: Map<string, boolean>;
  toolErrorKinds?: Map<string, string>;
  sessionId?: string;
  kbDomainId?: string;
}) {
  const { t } = useTranslation();
  const liveStatuses = useChatStore((s) => s.toolCallStatuses);
  const diagnosticCalls = calls.filter((tc) => {
    const live = liveStatuses?.[tc.id];
    const hasError = !!toolErrors?.get(tc.id);
    const isRecoverable = !!toolRecoverable?.get(tc.id) || live?.recoverable === true;
    return tc.name.startsWith('kb.') || hasError || isRecoverable || live?.status === 'running' || live?.status === 'error' || live?.requires_user_approval === true;
  });
  const compactCalls = calls.filter((tc) => !diagnosticCalls.some((item) => item.id === tc.id) && !tc.name.startsWith('kb.'));
  const compactSummary = getCompactToolSummary(compactCalls, toolResults);

  return (
    <div className="flex flex-col gap-2">
      {compactCalls.length > 0 && (
        <div className="rounded-xl border border-[var(--border-color)] bg-[var(--bg-card)] px-3 py-2">
          <div className="flex items-center gap-2 min-w-0">
            <Check className="w-3.5 h-3.5 shrink-0 text-[var(--success)]" aria-hidden="true" />
            <span className="text-[12px] font-medium text-[var(--text-primary)] truncate">
              {compactSummary}
            </span>
            {calls.length > 1 && (
              <span className="ml-auto shrink-0 rounded-full border border-[var(--border-color)] bg-[var(--bg-secondary)] px-2 py-0.5 text-[10px] font-semibold text-[var(--text-secondary)]">
                并行 x{calls.length}
              </span>
            )}
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {sessionId && (
              <DiagnosticLink href={`/sessions/${encodeURIComponent(sessionId)}/replay`} icon={<PlayCircle className="w-3 h-3" />}>
                {t('nav.replay', 'Replay')}
              </DiagnosticLink>
            )}
            {sessionId && (
              <DiagnosticLink href={`/sessions/${encodeURIComponent(sessionId)}/replay?trace=1`} icon={<Activity className="w-3 h-3" />}>
                Trace
              </DiagnosticLink>
            )}
            <DiagnosticLink href="/admin/quality-workbench" icon={<ShieldAlert className="w-3 h-3" />}>
              Admin
            </DiagnosticLink>
          </div>
        </div>
      )}

      {diagnosticCalls.map((tc) => (
        <ToolCallRow
          key={tc.id}
          id={tc.id}
          name={tc.name}
          args={tc.arguments}
          result={toolResults?.get(tc.id)}
          hasError={!!toolErrors?.get(tc.id)}
          recoverable={!!toolRecoverable?.get(tc.id)}
          errorKind={toolErrorKinds?.get(tc.id)}
          sessionId={sessionId}
          kbDomainId={kbDomainId}
        />
      ))}
    </div>
  );
}

function DiagnosticLink({ href, icon, children }: {
  href: string;
  icon: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <a
      href={href}
      className="inline-flex items-center gap-1 rounded-full border border-[var(--border-color)] px-2 py-1 text-[11px] font-medium text-[var(--text-secondary)] transition-colors hover:border-[var(--accent-border)] hover:text-[var(--accent-600)] dark:hover:text-[var(--accent-300)]"
    >
      {icon}
      {children}
    </a>
  );
}

/* ===== ToolCallRow：每个 tool_call_id 的行容器 =====
 * 接入 ai-elements <Tool> 外框（ToolAdapter）。原并列 chip+block 的 sibling 渲染
 * 被收进同一个 Collapsible：header 显示名字 + status badge，content 按 running/其他
 * 分别挂 chip 或 block。hasError 通过 ToolAdapter 映射到 output-error 并默认展开。
 */
function ToolCallRow({ id, name, args, result, hasError, recoverable, errorKind, sessionId, kbDomainId }: {
  id: string;
  name: string;
  args: string;
  result?: string;
  hasError: boolean;
  recoverable?: boolean;
  errorKind?: string;
  sessionId?: string;
  kbDomainId?: string;
}) {
  return (
    <ToolAdapter id={id} name={name} args={args} result={result} hasError={hasError} recoverable={recoverable} errorKind={errorKind} sessionId={sessionId} kbDomainId={kbDomainId} />
  );
}

/* ===== ToolCallCard: 已废弃 =====
 * 保留为 shim：内部 render <ToolCallRow/>；新代码请直接使用 ToolCallRow
 * 或 <ToolInvocationChip/> + <ToolExecutionBlock/>，不要再新增对 ToolCallCard 的引用。
 * 导出以保留外部兼容入口；真正的 JSX 调用点已在同文件内完成迁移。
 */
export function ToolCallCard({ id, name, args, result }: {
  id: string;
  name: string;
  args: string;
  status?: 'running' | 'success' | 'error';
  duration?: number;
  result?: string;
}) {
  useEffect(() => {
    if (import.meta.env.DEV) {
      console.warn('[deprecated] ToolCallCard: use <ToolInvocationChip /> + <ToolExecutionBlock /> instead');
    }
  }, []);
  return <ToolCallRow id={id} name={name} args={args} result={result} hasError={false} />;
}

// 检测内容是否为 HTML 文档
function isHtmlDocument(content: string): boolean {
  const trimmed = content.trimStart();
  return /^<!doctype\s+html/i.test(trimmed) || /^<html[\s>]/i.test(trimmed);
}

/* ===== 工具结果卡片（tool role 消息）===== */
function ToolResultCard({ status, preview, content, onCopy, copied, copyLabel, toolName }: {
  status: 'success' | 'error';
  preview: string;
  content: string;
  onCopy: () => void;
  copied: boolean;
  copyLabel: string;
  toolName?: string;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const isLong = content.length > 100;
  const isHtml = useMemo(() => isHtmlDocument(content), [content]);
  const imageUrl = useMemo(() => {
    try {
      const parsed = JSON.parse(content);
      if (parsed && typeof parsed.url === 'string') {
        const url = parsed.url;
        const isImageUrl =
          url.startsWith('data:image/') ||
          /\.(jpg|jpeg|png|gif|webp|svg)(\?|$)/i.test(url) ||
          url.startsWith('/api/images/');
        return isImageUrl ? url : null;
      }
    } catch {
      // not JSON, ignore
    }
    return null;
  }, [content]);
  const accent = getToolAccentByStatus(status);

  const openInCanvas = () => {
    const artifact: Artifact = {
      id: generateUUID(),
      title: toolName ? getToolDisplayName(toolName, t) : 'HTML Preview',
      language: 'html',
      content,
      type: 'html',
    };
    useCanvasStore.getState().openArtifact(artifact);
  };

  return (
    <div className="tool-result-card">
      <div
        className="flex items-center gap-2.5 px-4 py-3 cursor-pointer hover:bg-[var(--bg-hover)] transition-colors min-h-[44px]"
        onClick={() => isLong && setExpanded(!expanded)}
      >
        <Wrench className="w-3.5 h-3.5 shrink-0" style={{ color: accent }} />
        <span className="text-[12px] text-[var(--text-secondary)] flex-1 truncate">{preview}</span>
        <div className="flex items-center gap-1.5">
          {isHtml && (
            <button
              onClick={(e) => { e.stopPropagation(); openInCanvas(); }}
              title={t('chat.preview')}
              className="p-1 rounded transition-colors text-[var(--text-secondary)] hover:text-[var(--accent-600)] dark:hover:text-[var(--accent-300)] hover:bg-[var(--bg-hover)]"
            >
              <ExternalLink className="w-3.5 h-3.5" />
            </button>
          )}
          <button
            onClick={(e) => { e.stopPropagation(); onCopy(); }}
            title={copyLabel}
            className="p-1 rounded transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)] hover:bg-[var(--bg-hover)]"
          >
            {copied ? <Check className="w-3.5 h-3.5 text-emerald-500" /> : <Copy className="w-3.5 h-3.5" />}
          </button>
          {isLong && (
            <ChevronDown className={`w-3.5 h-3.5 text-[var(--text-secondary)] transition-transform ${expanded ? 'rotate-180' : ''}`} />
          )}
        </div>
      </div>
      {expanded && (
        <div className="px-3 pb-3 border-t border-[var(--border-color)]">
          <div className="mt-2 prose prose-xs dark:prose-invert" style={{ maxWidth: '100%', overflow: 'hidden', wordBreak: 'break-all', overflowWrap: 'anywhere' }}>
            <Streamdown
              plugins={STREAMDOWN_PLUGINS}
              allowedTags={ALLOWED_TAGS}
            >
              {content}
            </Streamdown>
          </div>
        </div>
      )}
      {imageUrl && (
        <div className="px-4 pb-3">
          <img
            src={imageUrl}
            alt="生成的图片"
            className="max-w-full rounded-lg border border-[var(--border-color)]"
            style={{ maxHeight: '512px', objectFit: 'contain' }}
          />
        </div>
      )}
    </div>
  );
}

/* ===== 错误消息折叠卡片 — Tailwind 工具类 DOM，对齐 ToolExecutionBlock 外观 ===== */
function ErrorCard({ content }: { content: string }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const summary = useMemo(() => {
    const firstLine = content.split('\n')[0];
    return firstLine.length > 60 ? firstLine.slice(0, 60) + '…' : firstLine;
  }, [content]);
  const toggleLabel = expanded ? t('tools.clickToCollapse') : t('tools.clickToExpand');

  return (
    <div className="rounded-2xl border border-[var(--border-color)] bg-[var(--bg-card)] overflow-hidden ring-1 ring-[var(--danger)]/30">
      <div
        className="flex items-center gap-2 px-3 py-2 min-h-[44px] cursor-pointer"
        onClick={() => setExpanded(!expanded)}
      >
        <AlertTriangle
          className="w-4 h-4 shrink-0"
          style={{ color: 'var(--danger)' }}
          aria-hidden="true"
        />
        <span className="text-[13px] font-semibold shrink-0" style={{ color: 'var(--danger)' }}>
          {t('tools.error')}
        </span>
        <span className="text-[12px] text-[var(--text-secondary)] truncate min-w-0">
          {summary}
        </span>
        <div className="flex items-center gap-2 ml-auto shrink-0">
          <X
            className="w-3.5 h-3.5"
            style={{ color: 'var(--danger)' }}
            aria-hidden="true"
          />
          <button
            type="button"
            onClick={(e) => { e.stopPropagation(); setExpanded(!expanded); }}
            aria-expanded={expanded}
            className="text-[11px] font-medium text-[var(--text-secondary)] hover:underline"
          >
            {toggleLabel}
          </button>
        </div>
      </div>
      {expanded && (
        <div className="border-t border-[var(--border-color)] px-3 py-2.5 bg-[var(--bg-secondary)]">
          <pre className="text-[12px] font-mono whitespace-pre-wrap break-words text-[var(--text-primary)] bg-[var(--bg-card)] border border-[var(--border-color)] rounded p-2">
            {content}
          </pre>
        </div>
      )}
    </div>
  );
}
