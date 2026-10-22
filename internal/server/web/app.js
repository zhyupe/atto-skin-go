(() => {
  'use strict';
  const $=selector=>document.querySelector(selector);
  let noticeTimer;
  function notice(message,error=false){
    const node=$('#notice');node.textContent=message;node.hidden=false;node.classList.add('show');node.classList.toggle('error',error);
    clearTimeout(noticeTimer);if(!error)noticeTimer=setTimeout(()=>{node.hidden=true;node.classList.remove('show');},5000);
  }
  for(const button of document.querySelectorAll('[data-copy]'))button.addEventListener('click',async()=>{
    const input=document.getElementById(button.dataset.copy);
    try{await navigator.clipboard.writeText(input.value);notice('已复制');}
    catch{input.focus();input.select();notice('请手动复制选中的内容。');}
  });
  for(const form of document.querySelectorAll('[data-confirm]'))form.addEventListener('submit',event=>{
    if(!window.confirm(form.dataset.confirm))event.preventDefault();
  });
  const dialogs=[...document.querySelectorAll('dialog.settings-panel')];
  const supportsDialogs=typeof HTMLDialogElement!=='undefined' && typeof HTMLDialogElement.prototype.showModal==='function';
  if(supportsDialogs){
    for(const dialog of dialogs){
      dialog.close();
      dialog.addEventListener('click',event=>{
        if(event.target!==dialog)return;
        const b=dialog.getBoundingClientRect();
        if(event.clientX<b.left||event.clientX>b.right||event.clientY<b.top||event.clientY>b.bottom)dialog.close();
      });
      dialog.addEventListener('close',()=>dialog.returnFocus?.focus());
    }
    for(const trigger of document.querySelectorAll('[data-open]'))trigger.addEventListener('click',event=>{
      const dialog=document.getElementById(trigger.dataset.open);
      if(!dialog)return;event.preventDefault();dialog.returnFocus=trigger;dialog.showModal();
    });
    for(const button of document.querySelectorAll('[data-close]'))button.addEventListener('click',()=>button.closest('dialog').close());
    document.documentElement.dataset.dialogs='true';
    const auto=dialogs.find(d=>d.hasAttribute('data-auto-open'));
    // Reopen the relevant settings after server-side validation errors.
    const errorPanel=location.pathname==='/account/profile'?$('#account-panel'):location.pathname==='/account/skin'?$('#upload-panel'):null;
    if(errorPanel && $('#server-message')){
      const error=document.createElement('p');error.className='form-error';error.setAttribute('role','alert');error.textContent=$('#server-message').textContent;
      errorPanel.querySelector('.dialog-head').after(error);
    }
    if(auto||errorPanel)(auto||errorPanel).showModal();
  }
  const renderer=window.AttoSkin, canvas=$('#character'), stage=$('.stage');
  if(!renderer || !canvas || !canvas.getContext('2d'))return;
  const loggedIn=stage.dataset.loggedIn==='true';
  const current={kind:'history',hash:stage.dataset.currentHash,model:stage.dataset.currentModel||'default',title:stage.dataset.currentHash?'当前皮肤':'默认外观'};
  let chosen=current, yaw=-.46, mesh=null, serial=0, busy=false;
  const textures=new Map();
  const load=url=>{if(!textures.has(url))textures.set(url,renderer.load(url).catch(error=>{textures.delete(url);throw error;}));return textures.get(url);};
  function repaint(){if(mesh)renderer.draw(canvas,mesh,yaw);}
  function isCurrent(s){return s.kind==='history'&&s.hash===current.hash&&s.model===current.model;}
  function updateUI(){
    if(!loggedIn)return;
    const same=isCurrent(chosen);
    $('#selected-name').textContent=chosen.title;
    $('#selected-model').textContent=chosen.hash||chosen.kind==='upload'?(chosen.model==='slim'?'纤细 · Alex':'经典 · Steve'):'尚未设置皮肤';
    $('.stage-state').textContent=same?(chosen.hash?'当前皮肤':'默认外观'):'试穿中 · 未保存';
    stage.dataset.previewState=same?'saved':'unsaved';
    $('#selection-hint').textContent=same&&!chosen.hash?'实际默认外观由游戏决定。':'先转一圈，看看新的自己。';
    $('#apply').disabled=busy||same;
    $('#apply').textContent=busy?'正在更换…':same?(chosen.hash?'正在使用':'选择一张皮肤'):'使用此皮肤 ↵';
    $('#cancel-preview').hidden=same;$('#cancel-preview').disabled=busy;
    const download=$('#download');download.hidden=!chosen.hash;
    if(chosen.hash)download.href='/textures/'+chosen.hash;else download.removeAttribute('href');
    for(const item of document.querySelectorAll('.skin-item')){
      const selected=chosen.kind==='history'&&chosen.hash===item.dataset.hash&&chosen.model===item.dataset.model;
      item.classList.toggle('selected',selected);item.querySelector('button').setAttribute('aria-pressed',String(selected));
    }
  }
  async function selectSkin(s){
    if(busy)return;
    chosen=s;updateUI();const ticket=++serial;mesh=null;
    canvas.getContext('2d').clearRect(0,0,canvas.width,canvas.height);
    const error=$('#preview-error');error.hidden=true;canvas.setAttribute('aria-busy','true');
    try{
      const texture=s.texture||(s.hash?await load('/textures/'+s.hash):renderer.mannequin());
      const next=renderer.buildMesh(texture,s.model);
      if(ticket!==serial)return;mesh=next;repaint();
    }catch{
      if(ticket!==serial)return;
      error.textContent='模型预览加载失败。你仍可下载 PNG 或使用此皮肤。';error.hidden=false;
    }finally{if(ticket===serial)canvas.removeAttribute('aria-busy');}
  }
  // Render thumbnails once, only when visible. There is no animation loop per skin.
  const thumbnails=[...document.querySelectorAll('canvas[data-skin]')];
  async function thumbnail(c){
    try{renderer.draw(c,renderer.buildMesh(await load(c.dataset.skin),c.dataset.model));}
    catch{const label=document.createElement('span');label.className='thumbnail-error';label.textContent='预览不可用';c.hidden=true;c.after(label);}
  }
  if('IntersectionObserver' in window){const observer=new IntersectionObserver(entries=>{for(const e of entries)if(e.isIntersecting){observer.unobserve(e.target);thumbnail(e.target);}},{rootMargin:'150px'});thumbnails.forEach(c=>observer.observe(c));}
  else thumbnails.forEach(thumbnail);
  if($('#avatar'))(current.hash?load('/textures/'+current.hash):Promise.resolve(renderer.mannequin())).then(t=>renderer.avatar($('#avatar'),t)).catch(()=>renderer.avatar($('#avatar'),renderer.mannequin()));
  let start=null, frame=0;
  canvas.addEventListener('pointerdown',event=>{start=event.clientX;canvas.setPointerCapture(event.pointerId);});
  canvas.addEventListener('pointermove',event=>{
    if(start===null)return;yaw+=(event.clientX-start)*.012;start=event.clientX;
    if(!frame)frame=requestAnimationFrame(()=>{frame=0;repaint();});
  });
  canvas.addEventListener('pointerup',()=>start=null);canvas.addEventListener('pointercancel',()=>start=null);canvas.addEventListener('lostpointercapture',()=>start=null);
  canvas.addEventListener('keydown',event=>{if(['ArrowLeft','ArrowRight'].includes(event.key)){event.preventDefault();yaw+=event.key==='ArrowLeft'?-.2:.2;repaint();}});
  $('#front').addEventListener('click',()=>{yaw=0;repaint();});$('#back').addEventListener('click',()=>{yaw=Math.PI;repaint();});
  if(loggedIn){
    async function submitSkin(action, data){
      if(busy)return;busy=true;updateUI();
      for(const b of document.querySelectorAll('.skin-select,[data-submit="skin"] button'))b.disabled=true;
      try{
        const response=await fetch(action,{method:'POST',body:data,credentials:'same-origin',headers:{Accept:'text/html'},signal:AbortSignal.timeout(30000)});
        if(response.ok && response.redirected){location.assign(response.url);return;}
        const body=await response.text();
        let message='保存失败，请稍后重试。';
        if(response.headers.get('Content-Type')?.includes('text/html'))message=new DOMParser().parseFromString(body,'text/html').querySelector('#server-message')?.textContent||message;
        else if(response.status===401)message='登录已过期，请刷新页面后重新登录。';
        else if(response.headers.get('Content-Type')?.includes('text/plain'))message=body.trim().slice(0,300)||message;
        throw new Error(message);
      }catch(error){
        notice(error.name==='TimeoutError'?'请求超时，请刷新页面确认是否保存成功。':error instanceof TypeError?'网络连接失败，试穿和文件已保留，请重试。':error.message,true);
      }finally{busy=false;for(const b of document.querySelectorAll('.skin-select,[data-submit="skin"] button'))b.disabled=false;updateUI();}
    }
    for(const form of document.querySelectorAll('.skin-item')){
      const button=form.querySelector('button');button.setAttribute('aria-label','试穿'+form.dataset.title);
      form.addEventListener('submit',event=>{
        event.preventDefault();if(busy)return;
        selectSkin({kind:'history',hash:form.dataset.hash,model:form.dataset.model,title:form.dataset.title});
        canvas.focus({preventScroll:true});stage.scrollIntoView({block:'start',behavior:'instant'});
      });
    }
    const upload=$('#upload-form'), file=$('#skin-file'), model=$('#model');
    $('#apply').addEventListener('click',()=>{
      if(isCurrent(chosen)||busy)return;
      if(chosen.kind==='upload'){
        // Keep the exact File and model that were previewed, even if the dialog was edited afterwards.
        const data=new FormData();data.set('csrf',upload.elements.csrf.value);data.set('file',chosen.file);data.set('model',chosen.model);
        submitSkin(upload.action,data);
      }else{
        const data=new FormData();data.set('csrf',upload.elements.csrf.value);data.set('hash',chosen.hash);data.set('model',chosen.model);
        submitSkin('/account/skin/restore',data);
      }
    });
    $('#cancel-preview').addEventListener('click',()=>{selectSkin(current);canvas.focus({preventScroll:true});});
    for(const form of document.querySelectorAll('[data-submit="skin"]'))form.addEventListener('submit',event=>{event.preventDefault();submitSkin(form.action,new FormData(form));});
    function fileChanged(){
      $('#file-name').textContent=file.files[0]?.name||'选择或拖入皮肤文件';$('#upload-error').hidden=true;
    }
    file.addEventListener('change',fileChanged);
    const drop=$('#dropzone');
    for(const name of ['dragenter','dragover'])drop.addEventListener(name,event=>{event.preventDefault();drop.classList.add('dragging');});
    drop.addEventListener('dragleave',()=>drop.classList.remove('dragging'));
    drop.addEventListener('drop',event=>{
      event.preventDefault();drop.classList.remove('dragging');
      if(event.dataTransfer.files.length!==1){$('#upload-error').textContent='请一次选择一张皮肤。';$('#upload-error').hidden=false;return;}
      file.files=event.dataTransfer.files;fileChanged();
    });
    let preparing=false;
    upload.addEventListener('submit',async event=>{
      event.preventDefault();if(preparing||busy)return;preparing=true;
      const button=$('#upload-submit');button.disabled=true;button.textContent='正在读取…';
      try{
        const selected=file.files[0],selectedModel=model.value;
        if(!selected||selected.size>1048576)throw new Error('请选择不超过 1 MiB 的 PNG 皮肤。');
        const header=await selected.slice(0,24).arrayBuffer();
        const signature=new Uint8Array(header).slice(0,8);
        if(signature.join(',')!=='137,80,78,71,13,10,26,10')throw new Error('文件不是有效的 PNG 图片。');
        if(header.byteLength!==24)throw new Error('PNG 文件不完整。');
        const dimensions=new DataView(header),width=dimensions.getUint32(16),height=dimensions.getUint32(20);
        if(width!==64||![32,64].includes(height))throw new Error('请使用 64×64 或 64×32 PNG 皮肤。');
        if(selectedModel==='slim'&&height===32)throw new Error('64×32 皮肤只支持经典 Steve 模型。');
        const url=URL.createObjectURL(selected);let texture;
        try{texture=await renderer.load(url);}finally{URL.revokeObjectURL(url);}
        if(selectedModel==='slim'&&texture.height===32)throw new Error('64×32 皮肤只支持经典 Steve 模型。');
        if(file.files[0]!==selected||model.value!==selectedModel)throw new Error('文件或模型已更改，请重新试穿。');
        await selectSkin({kind:'upload',hash:'',model:selectedModel,title:'新皮肤',texture,file:selected});
        $('#upload-error').hidden=true;if(supportsDialogs)$('#upload-panel').close();
        canvas.focus({preventScroll:true});stage.scrollIntoView({block:'start',behavior:'instant'});
      }catch(error){$('#upload-error').textContent=error.message||'无法读取皮肤，请重新选择 PNG。';$('#upload-error').hidden=false;}
      finally{preparing=false;button.disabled=false;button.textContent='试穿这张皮肤 ↵';}
    });
    $('#upload-submit').textContent='试穿这张皮肤 ↵';
  }
  document.documentElement.dataset.enhanced='true';
  selectSkin(current);
})();
