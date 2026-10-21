for (const button of document.querySelectorAll('[data-copy]')) {
  button.addEventListener('click', async () => {
    const input = document.getElementById(button.dataset.copy);
    const status = document.getElementById('copy-status');
    try { await navigator.clipboard.writeText(input.value); status.textContent = '已复制'; }
    catch { input.focus(); input.select(); status.textContent = '请手动复制选中的内容'; }
    setTimeout(() => { status.textContent = ''; }, 3000);
  });
}
for (const form of document.querySelectorAll('[data-confirm]')) {
  form.addEventListener('submit', event => { if (!window.confirm(form.dataset.confirm)) event.preventDefault(); });
}
for (const canvas of document.querySelectorAll('canvas[data-skin]')) {
  const image = new Image();
  image.onload = () => {
    const ctx = canvas.getContext('2d'); ctx.imageSmoothingEnabled = false;
    const scale = 8;
    const part = (sx,sy,w,h,x,y,flip=false) => {
      ctx.save();ctx.translate(x*scale,y*scale);
      if(flip){ctx.translate(w*scale,0);ctx.scale(-1,1);}
      ctx.drawImage(image,sx,sy,w,h,0,0,w*scale,h*scale);ctx.restore();
    };
    const arm = canvas.dataset.model === 'slim' ? 3 : 4;
    part(8,8,8,8,4,0);part(20,20,8,12,4,8);
    part(44,20,arm,12,4-arm,8);part(4,20,4,12,4,20);
    if(image.height===64){
      part(36,52,arm,12,12,8);part(20,52,4,12,8,20);
      part(20,36,8,12,4,8);part(44,36,arm,12,4-arm,8);
      part(52,52,arm,12,12,8);part(4,36,4,12,4,20);part(4,52,4,12,8,20);
    }else{part(44,20,arm,12,12,8,true);part(4,20,4,12,8,20,true);}
    part(40,8,8,8,4,0);
  };
  image.src = canvas.dataset.skin;
}
