const INVITE_INVALID_MSG = '邀请码无效或已失效，请联系管理员';

export function mapRegisterError(
  errorCode: string,
  message: string,
  inviteErrorWeakDistinction: boolean,
): string {
  if (errorCode === 'invite_expired' && inviteErrorWeakDistinction) {
    return '邀请码已过期，请联系管理员';
  }
  if (errorCode === 'registration_closed') {
    return '当前不允许自助注册，请联系管理员';
  }
  if (errorCode === 'email_already_registered') {
    return '该邮箱已注册，请直接登录或联系管理员';
  }
  if (errorCode === 'admin_requires_invite') {
    return '创建管理员账号需要有效邀请码';
  }
  if (errorCode === 'rate_limited') {
    return '请求过于频繁，请稍后再试';
  }
  if (errorCode === 'invite_invalid') {
    return INVITE_INVALID_MSG;
  }
  return message || '注册失败，请重试';
}
